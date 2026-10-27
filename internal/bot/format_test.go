package bot

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/qtekfun/vps-telegram-bot/internal/docker"
	"github.com/qtekfun/vps-telegram-bot/internal/gatus"
	"github.com/qtekfun/vps-telegram-bot/internal/hoststat"
)

func TestTruncateLinesKeepTail(t *testing.T) {
	in := "one-----xx\ntwo-----xx\nthree---xx\nfour-----x\n"
	if got := TruncateLines(in, 100, true); got != in {
		t.Fatal("short input changed")
	}
	got := TruncateLines(in, 40, true)
	if !strings.HasPrefix(got, "[... truncated ...]\n") || !strings.HasSuffix(got, "four-----x\n") || strings.Contains(got, "one") {
		t.Fatalf("%q", got)
	}
	if utf8.RuneCountInString(got) > 40 {
		t.Fatalf("too long: %d", utf8.RuneCountInString(got))
	}
}

func TestTruncateLinesFromBottom(t *testing.T) {
	got := TruncateLines("aaaa-----\nbbbb-----\ncccc-----\ndddd-----", 30, false)
	if !strings.HasPrefix(got, "aaaa") || !strings.HasSuffix(got, "[... truncated ...]") || strings.Contains(got, "dddd-") {
		t.Fatalf("%q", got)
	}
}

func TestTruncateHugeLineAndMultibyte(t *testing.T) {
	in := strings.Repeat("é", 5000)
	for _, tail := range []bool{true, false} {
		got := TruncateLines(in, 1000, tail)
		if n := utf8.RuneCountInString(got); n > 1000 || !utf8.ValidString(got) {
			t.Fatalf("tail=%v: %d runes", tail, n)
		}
	}
}

func TestCleanLog(t *testing.T) {
	got := CleanLog("a\x1b[1;31mred\x1b[0m\r\nb\xff")
	if got != "ared\nb�" {
		t.Fatalf("%q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		5 * time.Second: "5s", 5 * time.Minute: "5m", 3*time.Hour + 7*time.Minute: "3h07m",
		49*time.Hour + 30*time.Minute: "2d1h", -time.Second: "0s",
	}
	for d, want := range cases {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: %q want %q", d, got, want)
		}
	}
}

func TestFormatStatusStates(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mk := func(name, state, health string) ContainerView {
		return ContainerView{Container: docker.Container{Name: name, State: state}, InspectOK: true,
			Detail: &docker.Detail{State: state, Health: health, StartedAt: now.Add(-2 * time.Hour)}}
	}
	out := FormatStatus([]ContainerView{
		mk("zeta", "exited", ""), mk("alpha", "running", "unhealthy"), mk("mid<>", "running", ""),
		{Container: docker.Container{Name: "noinspect", State: "running"}},
	}, nil, hoststat.Info{
		MemTotal: 1000 << 20, MemAvailable: 250 << 20, Load1: 0.5, Uptime: 26 * time.Hour,
		Disks: []hoststat.Disk{{Path: "/data", Total: 100 << 30, Free: 40 << 30}, {Path: "/x", Err: errors.New("no")}},
	}, map[string]string{"wan": "down <now>"}, nil, now)
	for _, want := range []string{
		"⚠ <b>alpha</b> running/unhealthy", "✖ <b>zeta</b> exited", "<b>mid&lt;&gt;</b> running, restarts 0, up 2h00m",
		"<b>noinspect</b> running\n", "Memory: 750.0 MiB / 1000.0 MiB used (75%)", "Load: 0.50 0.00 0.00, uptime 1d2h",
		"Disk /data: 60.0 GiB / 100.0 GiB used (60%)", "Disk /x: unavailable", "wan: down &lt;now&gt;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Index(out, "alpha") > strings.Index(out, "zeta") {
		t.Error("containers not sorted")
	}
}

func TestFormatStatusErrorsAndNoAlerts(t *testing.T) {
	out := FormatStatus(nil, errors.New("proxy down"), hoststat.Info{}, nil, errors.New("bad json"), time.Now())
	if !strings.Contains(out, "Docker unavailable: proxy down") || !strings.Contains(out, "state file unreadable") {
		t.Fatal(out)
	}
	out = FormatStatus(nil, nil, hoststat.Info{}, map[string]string{}, nil, time.Now())
	if !strings.Contains(out, "none active") || strings.Contains(FormatStatus(nil, nil, hoststat.Info{}, nil, nil, time.Now()), "Alerts") {
		t.Fatal(out)
	}
}

func TestFormatStatusTruncatedToLimit(t *testing.T) {
	var vs []ContainerView
	for i := 0; i < 400; i++ {
		vs = append(vs, ContainerView{Container: docker.Container{Name: strings.Repeat("n", 30), State: "running"}})
	}
	if out := FormatStatus(vs, nil, hoststat.Info{}, nil, nil, time.Now()); utf8.RuneCountInString(out) > 4096 {
		t.Fatal("too long")
	}
}

func TestFormatChecks(t *testing.T) {
	out := FormatChecks([]gatus.Endpoint{
		{Group: "web", Name: "fine", HasData: true, Success: true},
		{Group: "web", Name: "broken", HasData: true, Failures: []string{"[STATUS] == 200"}},
		{Group: "", Name: "new"},
	})
	for _, want := range []string{"1 of 3 checks FAILING", "<b>(no group)</b>", "✖ <b>broken</b>\n    [STATUS] == 200", "new (no data yet)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if !strings.Contains(FormatChecks([]gatus.Endpoint{{Name: "a", HasData: true, Success: true}}), "All 1 checks passing") {
		t.Error("all-pass summary")
	}
	if !strings.Contains(FormatChecks(nil), "No endpoints") {
		t.Error("empty")
	}
}

func TestConfirmStore(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newConfirmStore(time.Minute, func() time.Time { return now })
	id := s.add(1, 2, "c", "stop")
	if _, r := s.take(id, 9, 2); r != takeForeign {
		t.Fatal("foreign user")
	}
	if _, r := s.take("nope", 1, 2); r != takeUnknown {
		t.Fatal("unknown")
	}
	p, r := s.take(id, 1, 2)
	if r != takeOK || p.Container != "c" || p.Action != "stop" {
		t.Fatal("redeem")
	}
	if _, r := s.take(id, 1, 2); r != takeUnknown {
		t.Fatal("single use")
	}
	id = s.add(1, 2, "c", "stop")
	now = now.Add(time.Minute + time.Second)
	if _, r := s.take(id, 1, 2); r != takeExpired {
		t.Fatal("expiry")
	}
	if id2 := s.add(1, 2, "c", "stop"); id2 == id || len(id2) != 16 {
		t.Fatal("ids must be unique and 16 hex chars")
	}
}
