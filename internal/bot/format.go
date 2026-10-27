package bot

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/qtekfun/vps-telegram-bot/internal/docker"
	"github.com/qtekfun/vps-telegram-bot/internal/gatus"
	"github.com/qtekfun/vps-telegram-bot/internal/hoststat"
)

// maxMessage stays below Telegram's 4096-character limit with some headroom.
const maxMessage = 3900

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func esc(s string) string { return html.EscapeString(s) }

// CleanLog makes raw container output safe to display: valid UTF-8, no ANSI
// colour codes, no carriage returns.
func CleanLog(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = ansiRe.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "\r", "")
}

// TruncateLines shortens s to at most max runes, dropping whole lines from the
// top (keepTail, for logs) or from the bottom, and marks the cut. A single line
// longer than max is cut by runes.
func TruncateLines(s string, max int, keepTail bool) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	marker := "[... truncated ...]\n"
	if !keepTail {
		marker = "\n[... truncated ...]"
	}
	budget := max - utf8.RuneCountInString(marker)
	lines := strings.SplitAfter(s, "\n")
	var kept []string
	used := 0
	for i := range lines {
		l := lines[i]
		if keepTail {
			l = lines[len(lines)-1-i]
		}
		n := utf8.RuneCountInString(l)
		if used+n > budget {
			if len(kept) == 0 { // one huge line: keep a slice of it
				r := []rune(l)
				if keepTail {
					l = string(r[len(r)-budget:])
				} else {
					l = string(r[:budget])
				}
				kept = append(kept, l)
			}
			break
		}
		kept = append(kept, l)
		used += n
	}
	if keepTail {
		for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
			kept[i], kept[j] = kept[j], kept[i]
		}
		return marker + strings.Join(kept, "")
	}
	return strings.TrimRight(strings.Join(kept, ""), "\n") + marker
}

// FormatDuration renders a duration compactly: 42s, 5m, 3h12m, 4d3h.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	default:
		return fmt.Sprintf("%dd%dh", s/86400, s%86400/3600)
	}
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// ContainerView joins the container list entry with its inspect data (which
// may be missing when inspect failed).
type ContainerView struct {
	docker.Container
	Detail    *docker.Detail
	InspectOK bool
}

func containerLine(c ContainerView, now time.Time) string {
	state, health := c.State, ""
	restarts := ""
	up := ""
	if c.InspectOK {
		state, health = c.Detail.State, c.Detail.Health
		restarts = fmt.Sprintf(", restarts %d", c.Detail.RestartCount)
		if state == "running" && !c.Detail.StartedAt.IsZero() {
			up = ", up " + FormatDuration(now.Sub(c.Detail.StartedAt))
		}
	}
	mark := "✔" // check
	switch {
	case state != "running":
		mark = "✖" // cross
	case health == "unhealthy":
		mark = "⚠" // warning
	case health == "starting":
		mark = "…"
	}
	desc := state
	if health != "" {
		desc += "/" + health
	}
	return fmt.Sprintf("%s <b>%s</b> %s%s%s", mark, esc(c.Name), esc(desc), restarts, up)
}

// FormatStatus builds the /status message.
func FormatStatus(cs []ContainerView, listErr error, host hoststat.Info, alerts map[string]string, alertsErr error, now time.Time) string {
	var b strings.Builder
	b.WriteString("<b>Containers</b>\n")
	if listErr != nil {
		fmt.Fprintf(&b, "Docker unavailable: %s\n", esc(listErr.Error()))
	}
	sorted := append([]ContainerView(nil), cs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, c := range sorted {
		b.WriteString(containerLine(c, now) + "\n")
	}
	if listErr == nil && len(cs) == 0 {
		b.WriteString("none\n")
	}

	b.WriteString("\n<b>Host</b>\n")
	if host.MemTotal > 0 {
		used := host.MemTotal - host.MemAvailable
		fmt.Fprintf(&b, "Memory: %s / %s used (%d%%)\n", humanBytes(used), humanBytes(host.MemTotal), used*100/host.MemTotal)
		if host.SwapTotal > 0 {
			fmt.Fprintf(&b, "Swap: %s / %s used\n", humanBytes(host.SwapTotal-host.SwapFree), humanBytes(host.SwapTotal))
		}
	}
	if host.Uptime > 0 {
		fmt.Fprintf(&b, "Load: %.2f %.2f %.2f, uptime %s\n", host.Load1, host.Load5, host.Load15, FormatDuration(host.Uptime))
	}
	for _, d := range host.Disks {
		switch {
		case d.Err != nil:
			fmt.Fprintf(&b, "Disk %s: unavailable\n", esc(d.Path))
		case d.Total == 0:
			fmt.Fprintf(&b, "Disk %s: n/a\n", esc(d.Path))
		default:
			used := d.Total - d.Free
			fmt.Fprintf(&b, "Disk %s: %s / %s used (%d%%)\n", esc(d.Path), humanBytes(used), humanBytes(d.Total), used*100/d.Total)
		}
	}
	for _, e := range host.Errs {
		fmt.Fprintf(&b, "%s\n", esc(e))
	}

	if alertsErr != nil {
		fmt.Fprintf(&b, "\n<b>Alerts</b>\nstate file unreadable: %s\n", esc(alertsErr.Error()))
	} else if alerts != nil {
		b.WriteString("\n<b>Alerts</b>\n")
		if len(alerts) == 0 {
			b.WriteString("none active\n")
		}
		for _, k := range hoststat.SortedKeys(alerts) {
			fmt.Fprintf(&b, "⚠ %s: %s\n", esc(k), esc(alerts[k]))
		}
	}
	return TruncateLines(strings.TrimRight(b.String(), "\n"), maxMessage, false)
}

// FormatChecks builds the /checks message: failing endpoints first in each
// group and a summary line on top.
func FormatChecks(eps []gatus.Endpoint) string {
	if len(eps) == 0 {
		return "No endpoints reported by Gatus."
	}
	failing := 0
	groups := map[string][]gatus.Endpoint{}
	var names []string
	for _, e := range eps {
		if e.HasData && !e.Success {
			failing++
		}
		g := e.Group
		if g == "" {
			g = "(no group)"
		}
		if _, ok := groups[g]; !ok {
			names = append(names, g)
		}
		groups[g] = append(groups[g], e)
	}
	sort.Strings(names)
	var b strings.Builder
	if failing == 0 {
		fmt.Fprintf(&b, "<b>All %d checks passing</b>\n", len(eps))
	} else {
		fmt.Fprintf(&b, "<b>%d of %d checks FAILING</b>\n", failing, len(eps))
	}
	for _, g := range names {
		es := groups[g]
		sort.SliceStable(es, func(i, j int) bool {
			fi, fj := es[i].HasData && !es[i].Success, es[j].HasData && !es[j].Success
			return fi && !fj
		})
		fmt.Fprintf(&b, "\n<b>%s</b>\n", esc(g))
		for _, e := range es {
			switch {
			case !e.HasData:
				fmt.Fprintf(&b, "… %s (no data yet)\n", esc(e.Name))
			case e.Success:
				fmt.Fprintf(&b, "✔ %s\n", esc(e.Name))
			default:
				fmt.Fprintf(&b, "✖ <b>%s</b>\n", esc(e.Name))
				for _, f := range e.Failures {
					fmt.Fprintf(&b, "    %s\n", esc(f))
				}
			}
		}
	}
	return TruncateLines(strings.TrimRight(b.String(), "\n"), maxMessage, false)
}
