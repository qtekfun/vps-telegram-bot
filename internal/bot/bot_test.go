package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/qtekfun/vps-telegram-bot/internal/docker"
	"github.com/qtekfun/vps-telegram-bot/internal/gatus"
	"github.com/qtekfun/vps-telegram-bot/internal/hoststat"
	"github.com/qtekfun/vps-telegram-bot/internal/telegram"
)

type sent struct {
	chat int64
	text string
	kb   *telegram.InlineKeyboard
}

type fakeTG struct {
	sent    []sent
	edits   []sent
	answers []string
	docs    []string
	docErr  error
}

func (f *fakeTG) SendMessage(_ context.Context, chat int64, text string, kb *telegram.InlineKeyboard) (int, error) {
	f.sent = append(f.sent, sent{chat, text, kb})
	return len(f.sent), nil
}
func (f *fakeTG) EditMessage(_ context.Context, chat int64, _ int, text string, kb *telegram.InlineKeyboard) error {
	f.edits = append(f.edits, sent{chat, text, kb})
	return nil
}
func (f *fakeTG) AnswerCallback(_ context.Context, _, text string) error {
	f.answers = append(f.answers, text)
	return nil
}
func (f *fakeTG) SendDocument(_ context.Context, _ int64, name string, data []byte, _ string) error {
	f.docs = append(f.docs, name+":"+string(data))
	return f.docErr
}

type fakeDocker struct {
	list    []docker.Container
	logs    string
	actions []string
	actErr  error
	tail    int
}

func (f *fakeDocker) List(context.Context) ([]docker.Container, error) { return f.list, nil }
func (f *fakeDocker) Inspect(context.Context, string) (docker.Detail, error) {
	return docker.Detail{State: "running", Health: "healthy", RestartCount: 2, StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeDocker) Logs(_ context.Context, _ string, tail int, _ bool) (string, error) {
	f.tail = tail
	return f.logs, nil
}
func (f *fakeDocker) Action(_ context.Context, id, action string) error {
	f.actions = append(f.actions, action+" "+id)
	return f.actErr
}

type fakeChecks struct{ eps []gatus.Endpoint }

func (f fakeChecks) Statuses(context.Context) ([]gatus.Endpoint, error) { return f.eps, nil }

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

const owner = int64(100)

func setup() (*Bot, *fakeTG, *fakeDocker, *clock) {
	tg := &fakeTG{}
	dk := &fakeDocker{list: []docker.Container{
		{ID: "aaaaaaaaaaaa", Name: "traefik", State: "running"},
		{ID: "bbbbbbbbbbbb", Name: "db", State: "running"},
	}}
	clk := &clock{t: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	b := New(tg, dk, fakeChecks{}, Options{
		Allowed:          map[int64]bool{owner: true},
		ActionContainers: []string{"traefik", "ghost"},
		ConfirmTTL:       60 * time.Second,
		Now:              clk.now,
		Host:             func() hoststat.Info { return hoststat.Info{MemTotal: 1 << 30, MemAvailable: 1 << 29} },
		Log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return b, tg, dk, clk
}

func msg(user int64, chatType, text string) telegram.Update {
	return telegram.Update{Message: &telegram.Message{MessageID: 1, From: &telegram.User{ID: user}, Chat: telegram.Chat{ID: user, Type: chatType}, Text: text}}
}

func cb(user int64, data string) telegram.Update {
	return telegram.Update{CallbackQuery: &telegram.CallbackQuery{ID: "q", From: telegram.User{ID: user},
		Message: &telegram.Message{MessageID: 5, Chat: telegram.Chat{ID: user, Type: "private"}}, Data: data}}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in, cmd string
		args    int
		ok      bool
	}{
		{"/status", "status", 0, true},
		{"/Logs@my_bot traefik 50", "logs", 2, true},
		{"  /help  ", "help", 0, true},
		{"hello", "", 0, false},
		{"/", "", 0, false},
		{"", "", 0, false},
	}
	for _, c := range cases {
		cmd, args, ok := ParseCommand(c.in)
		if cmd != c.cmd || len(args) != c.args || ok != c.ok {
			t.Errorf("%q -> %q %v %v", c.in, cmd, args, ok)
		}
	}
}

func TestUnauthorizedIgnoredSilently(t *testing.T) {
	var logbuf strings.Builder
	b, tg, dk, _ := setup()
	b.opt.Log = slog.New(slog.NewTextHandler(&logbuf, nil))
	b.Handle(context.Background(), msg(999, "private", "/status"))
	b.Handle(context.Background(), cb(999, "y:deadbeef"))
	b.Handle(context.Background(), telegram.Update{Message: &telegram.Message{Text: "/status"}}) // no sender
	if len(tg.sent)+len(tg.edits)+len(tg.answers) != 0 || len(dk.actions) != 0 {
		t.Fatalf("unauthorized user got a reaction: %+v", tg)
	}
	if !strings.Contains(logbuf.String(), "user_id=999") || strings.Contains(logbuf.String(), "/status") {
		t.Fatalf("log: %s", logbuf.String())
	}
}

func TestNonPrivateChatIgnored(t *testing.T) {
	b, tg, _, _ := setup()
	b.Handle(context.Background(), msg(owner, "group", "/status"))
	if len(tg.sent) != 0 {
		t.Fatal("answered in a group")
	}
}

func TestHelpAndUnknown(t *testing.T) {
	b, tg, _, _ := setup()
	b.Handle(context.Background(), msg(owner, "private", "/help"))
	b.Handle(context.Background(), msg(owner, "private", "/nope"))
	b.Handle(context.Background(), msg(owner, "private", "just text"))
	if len(tg.sent) != 2 || !strings.Contains(tg.sent[0].text, "/status") || !strings.Contains(tg.sent[1].text, "Unknown") {
		t.Fatalf("%+v", tg.sent)
	}
}

func TestStatus(t *testing.T) {
	b, tg, _, _ := setup()
	b.Handle(context.Background(), msg(owner, "private", "/status"))
	got := tg.sent[0].text
	for _, want := range []string{"<b>traefik</b> running/healthy, restarts 2, up 1d0h", "Memory: 512.0 MiB / 1.0 GiB used (50%)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestLogsInlineAndClamp(t *testing.T) {
	b, tg, dk, _ := setup()
	dk.logs = "line <1>\n\x1b[31mred\x1b[0m\n"
	b.Handle(context.Background(), msg(owner, "private", "/logs traefik 9999"))
	if dk.tail != 200 {
		t.Fatalf("tail %d", dk.tail)
	}
	got := tg.sent[0].text
	if !strings.Contains(got, "<pre>line &lt;1&gt;\nred</pre>") {
		t.Fatalf("%q", got)
	}
	b.Handle(context.Background(), msg(owner, "private", "/logs traefik"))
	if dk.tail != 30 {
		t.Fatalf("default tail %d", dk.tail)
	}
}

func TestLogsBadInput(t *testing.T) {
	b, tg, dk, _ := setup()
	for _, in := range []string{"/logs", "/logs nope", "/logs traefik abc", "/logs traefik 0", "/logs ../etc/passwd", "/logs a b c"} {
		b.Handle(context.Background(), msg(owner, "private", in))
	}
	if len(tg.sent) != 6 || dk.tail != 0 {
		t.Fatalf("sent %d tail %d", len(tg.sent), dk.tail)
	}
}

func TestLogsLongGoesToDocument(t *testing.T) {
	b, tg, dk, _ := setup()
	dk.logs = strings.Repeat("0123456789abcdef\n", 400)
	b.Handle(context.Background(), msg(owner, "private", "/logs db 200"))
	if len(tg.docs) != 1 || !strings.HasPrefix(tg.docs[0], "db.log.txt:") || len(tg.sent) != 0 {
		t.Fatalf("docs %d sent %d", len(tg.docs), len(tg.sent))
	}
	tg.docErr = errors.New("boom")
	b.Handle(context.Background(), msg(owner, "private", "/logs db 200"))
	if len(tg.sent) != 1 || !strings.Contains(tg.sent[0].text, "truncated") || len([]rune(tg.sent[0].text)) > 4096 {
		t.Fatal("fallback to truncated message failed")
	}
}

func TestChecks(t *testing.T) {
	b, tg, _, _ := setup()
	b.checks = fakeChecks{eps: []gatus.Endpoint{
		{Group: "web", Name: "ok", HasData: true, Success: true},
		{Group: "web", Name: "bad", HasData: true, Failures: []string{"[STATUS] == 200"}},
	}}
	b.Handle(context.Background(), msg(owner, "private", "/checks"))
	got := tg.sent[0].text
	if !strings.Contains(got, "1 of 2 checks FAILING") || strings.Index(got, "bad") > strings.Index(got, "ok") {
		t.Fatalf("%s", got)
	}
	b.checks = nil
	b.Handle(context.Background(), msg(owner, "private", "/checks"))
	if !strings.Contains(tg.sent[1].text, "not configured") {
		t.Fatal(tg.sent[1].text)
	}
}

func TestContainersMenuListsOnlyAllowlist(t *testing.T) {
	b, tg, _, _ := setup()
	b.Handle(context.Background(), msg(owner, "private", "/containers"))
	rows := tg.sent[0].kb.Rows
	if len(rows) != 2 || rows[0][0].Text != "traefik (running)" || rows[1][0].Text != "ghost (not found)" {
		t.Fatalf("%+v", rows)
	}
}

// confirmID drives the menu to the Yes/No prompt and returns the confirmation id.
func confirmID(t *testing.T, b *Bot, tg *fakeTG, user int64, data string) string {
	t.Helper()
	b.Handle(context.Background(), cb(user, data))
	last := tg.edits[len(tg.edits)-1]
	if last.kb == nil || len(last.kb.Rows) != 1 || len(last.kb.Rows[0]) != 2 {
		t.Fatalf("no confirmation prompt: %+v", last)
	}
	yes := last.kb.Rows[0][0].CallbackData
	if !strings.HasPrefix(yes, "y:") || len(yes) > 64 {
		t.Fatalf("bad callback data %q", yes)
	}
	return strings.TrimPrefix(yes, "y:")
}

func TestRestartFlow(t *testing.T) {
	b, tg, dk, _ := setup()
	id := confirmID(t, b, tg, owner, "a:restart:0")
	if !strings.Contains(tg.edits[0].text, "Restart <b>traefik</b>?") || len(dk.actions) != 0 {
		t.Fatalf("no confirmation before acting: %+v %v", tg.edits, dk.actions)
	}
	b.Handle(context.Background(), cb(owner, "y:"+id))
	if len(dk.actions) != 1 || dk.actions[0] != "restart aaaaaaaaaaaa" {
		t.Fatalf("actions %v", dk.actions)
	}
	if !strings.Contains(tg.edits[1].text, "restarted") {
		t.Fatal(tg.edits[1].text)
	}
	// Single use.
	b.Handle(context.Background(), cb(owner, "y:"+id))
	if len(dk.actions) != 1 {
		t.Fatal("confirmation reused")
	}
}

func TestConfirmExpires(t *testing.T) {
	b, tg, dk, clk := setup()
	id := confirmID(t, b, tg, owner, "a:stop:0")
	clk.t = clk.t.Add(61 * time.Second)
	b.Handle(context.Background(), cb(owner, "y:"+id))
	if len(dk.actions) != 0 || !strings.Contains(tg.edits[len(tg.edits)-1].text, "expired") {
		t.Fatalf("expired confirmation executed: %v", dk.actions)
	}
}

func TestConfirmBoundToUserAndChat(t *testing.T) {
	b, tg, dk, _ := setup()
	b.opt.Allowed[200] = true // a second authorized user must not redeem the owner's prompt
	id := confirmID(t, b, tg, owner, "a:start:0")
	b.Handle(context.Background(), cb(200, "y:"+id))
	if len(dk.actions) != 0 {
		t.Fatal("foreign user executed the action")
	}
	// Same user, different chat.
	other := cb(owner, "y:"+id)
	other.CallbackQuery.Message.Chat.ID = 12345
	b.Handle(context.Background(), other)
	if len(dk.actions) != 0 {
		t.Fatal("other chat executed the action")
	}
	// The rightful owner can still confirm afterwards.
	b.Handle(context.Background(), cb(owner, "y:"+id))
	if len(dk.actions) != 1 {
		t.Fatal("owner could not confirm")
	}
}

func TestCancel(t *testing.T) {
	b, tg, dk, _ := setup()
	id := confirmID(t, b, tg, owner, "a:stop:0")
	b.Handle(context.Background(), cb(owner, "n:"+id))
	b.Handle(context.Background(), cb(owner, "y:"+id))
	if len(dk.actions) != 0 || tg.edits[1].text != "Cancelled." {
		t.Fatalf("%v %+v", dk.actions, tg.edits)
	}
}

func TestMaliciousCallbacksRejected(t *testing.T) {
	b, tg, dk, _ := setup()
	for _, data := range []string{
		"a:restart:2", "a:restart:-1", "a:restart:abc", "a:kill:0", "a:exec:0", "a:restart", "a:",
		"m:9", "m:x", "y:", "y:../../x", "n:zzz", "zzz", "",
	} {
		b.Handle(context.Background(), cb(owner, data))
	}
	if len(dk.actions) != 0 {
		t.Fatalf("actions executed: %v", dk.actions)
	}
	for _, e := range tg.edits {
		if e.kb != nil && len(e.kb.Rows) > 0 && strings.HasPrefix(e.kb.Rows[0][0].CallbackData, "y:") {
			t.Fatalf("confirmation created for invalid input: %+v", e)
		}
	}
}

func TestActionOnMissingContainerFails(t *testing.T) {
	b, tg, dk, _ := setup()
	id := confirmID(t, b, tg, owner, "a:start:1") // "ghost" is allowlisted but not running on the host
	b.Handle(context.Background(), cb(owner, "y:"+id))
	if len(dk.actions) != 0 || !strings.Contains(tg.edits[len(tg.edits)-1].text, "not available") {
		t.Fatalf("%v %+v", dk.actions, tg.edits)
	}
}

func TestActionErrorReported(t *testing.T) {
	b, tg, dk, _ := setup()
	dk.actErr = errors.New("proxy said no <x>")
	id := confirmID(t, b, tg, owner, "a:stop:0")
	b.Handle(context.Background(), cb(owner, "y:"+id))
	if !strings.Contains(tg.edits[len(tg.edits)-1].text, "failed: proxy said no &lt;x&gt;") {
		t.Fatal(tg.edits[len(tg.edits)-1].text)
	}
}

func TestEmptyAllowlist(t *testing.T) {
	b, tg, _, _ := setup()
	b.opt.ActionContainers = nil
	b.Handle(context.Background(), msg(owner, "private", "/containers"))
	if !strings.Contains(tg.sent[0].text, "ACTION_CONTAINERS") || tg.sent[0].kb != nil {
		t.Fatal(tg.sent[0])
	}
}

func TestCommandsMatchBotFatherList(t *testing.T) {
	var names []string
	for _, c := range Commands() {
		names = append(names, c.Command)
	}
	if strings.Join(names, ",") != "status,logs,containers,checks,help" {
		t.Fatal(names)
	}
}
