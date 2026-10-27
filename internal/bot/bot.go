// Package bot implements the command handling, authorization and confirmation
// flow, independent of the real Telegram and Docker transports.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/qtekfun/vps-telegram-bot/internal/docker"
	"github.com/qtekfun/vps-telegram-bot/internal/gatus"
	"github.com/qtekfun/vps-telegram-bot/internal/hoststat"
	"github.com/qtekfun/vps-telegram-bot/internal/telegram"
)

const (
	defaultLogLines = 30
	maxLogLines     = 200
	updateTimeout   = 45 * time.Second
)

// Messenger is the part of the Telegram client the bot uses.
type Messenger interface {
	SendMessage(ctx context.Context, chatID int64, text string, kb *telegram.InlineKeyboard) (int, error)
	EditMessage(ctx context.Context, chatID int64, msgID int, text string, kb *telegram.InlineKeyboard) error
	AnswerCallback(ctx context.Context, id, text string) error
	SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string) error
}

// Docker is the part of the Docker client the bot uses.
type Docker interface {
	List(ctx context.Context) ([]docker.Container, error)
	Inspect(ctx context.Context, id string) (docker.Detail, error)
	Logs(ctx context.Context, id string, tail int, tty bool) (string, error)
	Action(ctx context.Context, id, action string) error
}

// Checks provides Gatus endpoint statuses.
type Checks interface {
	Statuses(ctx context.Context) ([]gatus.Endpoint, error)
}

type Options struct {
	Allowed          map[int64]bool
	ActionContainers []string
	ConfirmTTL       time.Duration
	Now              func() time.Time
	Host             func() hoststat.Info
	Alerts           func() (map[string]string, error)
	Log              *slog.Logger
}

type Bot struct {
	tg      Messenger
	dk      Docker
	checks  Checks // may be nil
	opt     Options
	confirm *confirmStore
}

func New(tg Messenger, dk Docker, checks Checks, opt Options) *Bot {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Host == nil {
		opt.Host = func() hoststat.Info { return hoststat.Info{} }
	}
	if opt.Alerts == nil {
		opt.Alerts = func() (map[string]string, error) { return nil, nil }
	}
	if opt.ConfirmTTL <= 0 {
		opt.ConfirmTTL = 60 * time.Second
	}
	return &Bot{tg: tg, dk: dk, checks: checks, opt: opt, confirm: newConfirmStore(opt.ConfirmTTL, opt.Now)}
}

// Commands is the list to register with setMyCommands (and in BotFather).
func Commands() []telegram.Command {
	return []telegram.Command{
		{Command: "status", Description: "Containers, host and alerts"},
		{Command: "logs", Description: "Container logs: /logs name [lines]"},
		{Command: "containers", Description: "Start, stop or restart a container"},
		{Command: "checks", Description: "Gatus endpoint status"},
		{Command: "help", Description: "Show help"},
	}
}

// ParseCommand splits "/cmd@botname arg1 arg2" into a lower-case command (no
// slash, no bot suffix) and its arguments. ok is false for non-commands.
func ParseCommand(text string) (cmd string, args []string, ok bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", nil, false
	}
	cmd = strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	if cmd == "" {
		return "", nil, false
	}
	return cmd, fields[1:], true
}

// Handle processes one update. Unauthorized senders are ignored silently; only
// their numeric ID is logged.
func (b *Bot) Handle(ctx context.Context, u telegram.Update) {
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	switch {
	case u.Message != nil:
		m := u.Message
		if m.From == nil || !b.authorized(m.From.ID) {
			b.logIgnored(m.From)
			return
		}
		if m.Chat.Type != "private" {
			return
		}
		b.handleMessage(ctx, m)
	case u.CallbackQuery != nil:
		q := u.CallbackQuery
		if !b.authorized(q.From.ID) {
			b.logIgnored(&q.From)
			return
		}
		if q.Message == nil || q.Message.Chat.Type != "private" {
			return
		}
		b.handleCallback(ctx, q)
	}
}

func (b *Bot) authorized(id int64) bool { return b.opt.Allowed[id] }

func (b *Bot) logIgnored(u *telegram.User) {
	if u == nil {
		return
	}
	b.opt.Log.Info("ignored update from unauthorized user", "user_id", u.ID)
}

func (b *Bot) send(ctx context.Context, chatID int64, text string, kb *telegram.InlineKeyboard) {
	if _, err := b.tg.SendMessage(ctx, chatID, TruncateLines(text, maxMessage, false), kb); err != nil {
		b.opt.Log.Warn("send failed", "err", err)
	}
}

func (b *Bot) edit(ctx context.Context, chatID int64, msgID int, text string, kb *telegram.InlineKeyboard) {
	if err := b.tg.EditMessage(ctx, chatID, msgID, TruncateLines(text, maxMessage, false), kb); err != nil {
		b.opt.Log.Warn("edit failed", "err", err)
	}
}

func (b *Bot) handleMessage(ctx context.Context, m *telegram.Message) {
	cmd, args, ok := ParseCommand(m.Text)
	if !ok {
		return
	}
	chat := m.Chat.ID
	switch cmd {
	case "start", "help":
		b.send(ctx, chat, helpText, nil)
	case "status":
		b.send(ctx, chat, b.status(ctx), nil)
	case "logs":
		b.logs(ctx, chat, args)
	case "containers":
		text, kb := b.containersMenu(ctx)
		b.send(ctx, chat, text, kb)
	case "checks":
		b.send(ctx, chat, b.checksText(ctx), nil)
	default:
		b.send(ctx, chat, "Unknown command. Try /help.", nil)
	}
}

const helpText = `<b>Commands</b>
/status - containers, host and active alerts
/logs <i>name</i> [lines] - last lines of a container's log (default 30, max 200)
/containers - start, stop or restart an allowed container (asks for confirmation)
/checks - Gatus endpoint status
/help - this message`

func (b *Bot) status(ctx context.Context) string {
	var views []ContainerView
	list, listErr := b.dk.List(ctx)
	for _, c := range list {
		v := ContainerView{Container: c}
		if d, err := b.dk.Inspect(ctx, c.ID); err == nil {
			v.Detail, v.InspectOK = &d, true
		}
		views = append(views, v)
	}
	alerts, alertsErr := b.opt.Alerts()
	return FormatStatus(views, listErr, b.opt.Host(), alerts, alertsErr, b.opt.Now())
}

func (b *Bot) checksText(ctx context.Context) string {
	if b.checks == nil {
		return "Gatus is not configured (set GATUS_URL)."
	}
	eps, err := b.checks.Statuses(ctx)
	if err != nil {
		return "Could not query Gatus: " + esc(err.Error())
	}
	return FormatChecks(eps)
}

// find resolves a user-supplied name against the real container list.
func find(list []docker.Container, name string) (docker.Container, bool) {
	for _, c := range list {
		if c.Name == name {
			return c, true
		}
	}
	return docker.Container{}, false
}

func (b *Bot) logs(ctx context.Context, chat int64, args []string) {
	list, err := b.dk.List(ctx)
	if err != nil {
		b.send(ctx, chat, "Docker error: "+esc(err.Error()), nil)
		return
	}
	if len(args) == 0 || len(args) > 2 {
		b.send(ctx, chat, "Usage: /logs <i>name</i> [lines]\nContainers: "+esc(names(list)), nil)
		return
	}
	n := defaultLogLines
	if len(args) == 2 {
		v, err := strconv.Atoi(args[1])
		if err != nil || v < 1 {
			b.send(ctx, chat, "Lines must be a positive number (max 200).", nil)
			return
		}
		n = min(v, maxLogLines)
	}
	c, ok := find(list, args[0])
	if !ok {
		b.send(ctx, chat, "Unknown container. Containers: "+esc(names(list)), nil)
		return
	}
	d, err := b.dk.Inspect(ctx, c.ID)
	if err != nil {
		b.send(ctx, chat, "Docker error: "+esc(err.Error()), nil)
		return
	}
	raw, err := b.dk.Logs(ctx, c.ID, n, d.Tty)
	if err != nil {
		b.send(ctx, chat, "Docker error: "+esc(err.Error()), nil)
		return
	}
	text := strings.TrimRight(CleanLog(raw), "\n")
	if text == "" {
		text = "(no output)"
	}
	header := fmt.Sprintf("<b>%s</b> - last %d lines\n", esc(c.Name), n)
	const inlineLimit = maxMessage - 200
	if len([]rune(text)) <= inlineLimit {
		b.send(ctx, chat, header+"<pre>"+esc(text)+"</pre>", nil)
		return
	}
	if err := b.tg.SendDocument(ctx, chat, c.Name+".log.txt", []byte(text+"\n"), fmt.Sprintf("%s - last %d lines", c.Name, n)); err != nil {
		b.opt.Log.Warn("sending log document failed", "err", err)
		b.send(ctx, chat, header+"<pre>"+esc(TruncateLines(text, inlineLimit, true))+"</pre>", nil)
	}
}

func names(list []docker.Container) string {
	var ns []string
	for _, c := range list {
		ns = append(ns, c.Name)
	}
	if len(ns) == 0 {
		return "none"
	}
	return strings.Join(ns, ", ")
}

// --- start / stop / restart ---------------------------------------------------

var verbs = map[string]string{"start": "Start", "stop": "Stop", "restart": "Restart"}
var pastTense = map[string]string{"start": "started", "stop": "stopped", "restart": "restarted"}

func (b *Bot) containersMenu(ctx context.Context) (string, *telegram.InlineKeyboard) {
	if len(b.opt.ActionContainers) == 0 {
		return "No containers are enabled for actions (set ACTION_CONTAINERS).", nil
	}
	list, err := b.dk.List(ctx)
	if err != nil {
		return "Docker error: " + esc(err.Error()), nil
	}
	kb := &telegram.InlineKeyboard{}
	for i, name := range b.opt.ActionContainers {
		state := "not found"
		if c, ok := find(list, name); ok {
			state = c.State
		}
		kb.Rows = append(kb.Rows, []telegram.Button{{Text: name + " (" + state + ")", CallbackData: "m:" + strconv.Itoa(i)}})
	}
	return "Choose a container:", kb
}

func (b *Bot) actionName(idx string) (string, bool) {
	i, err := strconv.Atoi(idx)
	if err != nil || i < 0 || i >= len(b.opt.ActionContainers) {
		return "", false
	}
	return b.opt.ActionContainers[i], true
}

func (b *Bot) answer(ctx context.Context, id, text string) {
	if err := b.tg.AnswerCallback(ctx, id, text); err != nil {
		b.opt.Log.Warn("answerCallbackQuery failed", "err", err)
	}
}

func (b *Bot) handleCallback(ctx context.Context, q *telegram.CallbackQuery) {
	chat, msg, user := q.Message.Chat.ID, q.Message.MessageID, q.From.ID
	kind, rest, _ := strings.Cut(q.Data, ":")
	switch kind {
	case "b": // back to the container list
		b.answer(ctx, q.ID, "")
		text, kb := b.containersMenu(ctx)
		b.edit(ctx, chat, msg, text, kb)
	case "m": // container submenu
		b.answer(ctx, q.ID, "")
		name, ok := b.actionName(rest)
		if !ok {
			b.edit(ctx, chat, msg, "Unknown container.", nil)
			return
		}
		b.edit(ctx, chat, msg, "<b>"+esc(name)+"</b>\nChoose an action:", &telegram.InlineKeyboard{Rows: [][]telegram.Button{
			{
				{Text: "Start", CallbackData: "a:start:" + rest},
				{Text: "Stop", CallbackData: "a:stop:" + rest},
				{Text: "Restart", CallbackData: "a:restart:" + rest},
			},
			{{Text: "Back", CallbackData: "b"}},
		}})
	case "a": // ask for confirmation
		b.answer(ctx, q.ID, "")
		verb, idx, _ := strings.Cut(rest, ":")
		name, ok := b.actionName(idx)
		if _, known := verbs[verb]; !ok || !known {
			b.edit(ctx, chat, msg, "Unknown action.", nil)
			return
		}
		id := b.confirm.add(user, chat, name, verb)
		b.edit(ctx, chat, msg, fmt.Sprintf("%s <b>%s</b>?\nThis request expires in %d s.", verbs[verb], esc(name), int(b.opt.ConfirmTTL.Seconds())),
			&telegram.InlineKeyboard{Rows: [][]telegram.Button{{
				{Text: "Yes, " + strings.ToLower(verbs[verb]), CallbackData: "y:" + id},
				{Text: "No", CallbackData: "n:" + id},
			}}})
	case "y":
		b.execute(ctx, q, rest)
	case "n":
		if b.confirm.cancel(rest, user, chat) {
			b.answer(ctx, q.ID, "")
			b.edit(ctx, chat, msg, "Cancelled.", nil)
		} else {
			b.answer(ctx, q.ID, "Not available")
		}
	default:
		b.answer(ctx, q.ID, "")
	}
}

func (b *Bot) execute(ctx context.Context, q *telegram.CallbackQuery, id string) {
	chat, msg, user := q.Message.Chat.ID, q.Message.MessageID, q.From.ID
	p, res := b.confirm.take(id, user, chat)
	switch res {
	case takeForeign:
		b.answer(ctx, q.ID, "This confirmation is not yours")
		return
	case takeUnknown, takeExpired:
		b.answer(ctx, q.ID, "Expired")
		b.edit(ctx, chat, msg, "This confirmation expired or was already used. Start again with /containers.", nil)
		return
	}
	b.answer(ctx, q.ID, "")
	// Re-check the allowlist and resolve the name against the live container list.
	allowed := false
	for _, n := range b.opt.ActionContainers {
		allowed = allowed || n == p.Container
	}
	list, err := b.dk.List(ctx)
	if err != nil {
		b.edit(ctx, chat, msg, "Docker error: "+esc(err.Error()), nil)
		return
	}
	c, found := find(list, p.Container)
	if !allowed || !found {
		b.edit(ctx, chat, msg, "Container <b>"+esc(p.Container)+"</b> is not available.", nil)
		return
	}
	b.opt.Log.Info("container action", "user_id", user, "container", c.Name, "action", p.Action)
	if err := b.dk.Action(ctx, c.ID, p.Action); err != nil {
		b.opt.Log.Warn("container action failed", "container", c.Name, "action", p.Action, "err", err)
		b.edit(ctx, chat, msg, fmt.Sprintf("%s <b>%s</b> failed: %s", verbs[p.Action], esc(c.Name), esc(err.Error())), nil)
		return
	}
	b.edit(ctx, chat, msg, fmt.Sprintf("<b>%s</b> %s.", esc(c.Name), pastTense[p.Action]), nil)
}
