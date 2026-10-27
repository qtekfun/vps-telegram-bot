package hoststat

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRead(t *testing.T) {
	d := t.TempDir()
	write(t, d, "meminfo", "MemTotal:        2048 kB\nMemFree: 1 kB\nMemAvailable:    1024 kB\nSwapTotal: 100 kB\nSwapFree: 50 kB\n")
	write(t, d, "loadavg", "0.10 0.50 1.25 1/200 1234\n")
	write(t, d, "uptime", "3600.5 7000.1\n")
	in := Read(d, []string{d, "/definitely/not/here"})
	if len(in.Errs) != 0 {
		t.Fatal(in.Errs)
	}
	if in.MemTotal != 2048*1024 || in.MemAvailable != 1024*1024 || in.SwapFree != 50*1024 {
		t.Fatalf("%+v", in)
	}
	if in.Load1 != 0.10 || in.Load15 != 1.25 || in.Uptime != 3600500*time.Millisecond {
		t.Fatalf("%+v", in)
	}
	if in.Disks[0].Total == 0 || in.Disks[0].Err != nil || in.Disks[1].Err == nil {
		t.Fatalf("%+v", in.Disks)
	}
}

func TestReadMissingProc(t *testing.T) {
	in := Read(t.TempDir(), nil)
	if len(in.Errs) != 3 {
		t.Fatalf("%v", in.Errs)
	}
}

func TestAlerts(t *testing.T) {
	d := t.TempDir()
	if a, err := Alerts(filepath.Join(d, "none.json")); a != nil || err != nil {
		t.Fatalf("missing: %v %v", a, err)
	}
	if a, err := Alerts(""); a != nil || err != nil {
		t.Fatal("empty path")
	}
	write(t, d, "s.json", `{"active":{"b":"two","a":"one"},"other":1}`)
	a, err := Alerts(filepath.Join(d, "s.json"))
	if err != nil || len(a) != 2 || SortedKeys(a)[0] != "a" {
		t.Fatalf("%v %v", a, err)
	}
	write(t, d, "bad.json", `nope`)
	if _, err := Alerts(filepath.Join(d, "bad.json")); err == nil {
		t.Fatal("expected error")
	}
}
