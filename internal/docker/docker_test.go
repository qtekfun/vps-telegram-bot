package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestDemux(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(frame(1, "out line\n"))
	buf.Write(frame(2, "err line\n"))
	buf.Write(frame(1, ""))
	got, err := Demux(&buf)
	if err != nil || string(got) != "out line\nerr line\n" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestDemuxTruncatedAndBad(t *testing.T) {
	b := frame(1, "abcdef")
	got, err := Demux(bytes.NewReader(b[:11])) // header + 3 bytes
	if err != nil || string(got) != "abc" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Demux(strings.NewReader("plain text, not multiplexed")); err == nil {
		t.Fatal("expected format error")
	}
}

const id1 = "aaaaaaaaaaaa1111"

func newFake(t *testing.T, h http.HandlerFunc) *Client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New("tcp://"+strings.TrimPrefix(srv.URL, "http://"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRejectsOtherSchemes(t *testing.T) {
	for _, h := range []string{"unix:///var/run/docker.sock", "ssh://x", "nonsense", ""} {
		if _, err := New(h, nil); err == nil {
			t.Errorf("%q accepted", h)
		}
	}
}

func TestListInspect(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			if r.URL.Query().Get("all") != "1" {
				t.Error("all=1 missing")
			}
			io.WriteString(w, `[{"Id":"`+id1+`","Names":["/web/alias","/web"],"State":"running"},{"Id":"x","Names":[],"State":"exited"}]`)
		case "/containers/" + id1 + "/json":
			io.WriteString(w, `{"RestartCount":3,"State":{"Status":"running","StartedAt":"2026-01-02T03:04:05.5Z","Health":{"Status":"healthy"}},"Config":{"Tty":true}}`)
		}
	})
	cs, err := c.List(context.Background())
	if err != nil || len(cs) != 1 || cs[0].Name != "web" || cs[0].State != "running" {
		t.Fatalf("%v %+v", err, cs)
	}
	d, err := c.Inspect(context.Background(), id1)
	if err != nil || d.RestartCount != 3 || d.Health != "healthy" || !d.Tty || d.StartedAt.IsZero() {
		t.Fatalf("%v %+v", err, d)
	}
}

func TestLogs(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/"+id1+"/logs" || r.URL.Query().Get("tail") != "5" {
			t.Errorf("bad request %s", r.URL)
		}
		w.Write(frame(1, "hello\n"))
	})
	got, err := c.Logs(context.Background(), id1, 5, false)
	if err != nil || got != "hello\n" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestActions(t *testing.T) {
	var calls []string
	status := http.StatusNoContent
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.WriteHeader(status)
		if status >= 400 {
			io.WriteString(w, `{"message":"boom"}`)
		}
	})
	ctx := context.Background()
	for _, a := range []string{"start", "stop", "restart"} {
		if err := c.Action(ctx, id1, a); err != nil {
			t.Fatal(a, err)
		}
	}
	want := "POST /containers/" + id1 + "/restart"
	if calls[2] != want {
		t.Fatalf("calls %v", calls)
	}
	status = http.StatusNotModified
	if err := c.Action(ctx, id1, "start"); err != nil {
		t.Fatalf("304 should be ok: %v", err)
	}
	status = http.StatusInternalServerError
	if err := c.Action(ctx, id1, "stop"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	for _, bad := range []string{"kill", "exec", "../x"} {
		if err := c.Action(ctx, id1, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := c.Action(ctx, "../../etc", "start"); err == nil {
		t.Error("bad id accepted")
	}
	if len(calls) != 5 {
		t.Fatalf("unexpected calls: %v", calls)
	}
}
