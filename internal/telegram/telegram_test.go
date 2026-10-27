package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetUpdatesAndSend(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if !strings.Contains(string(body), `"timeout":1`) {
				t.Errorf("timeout missing: %s", body)
			}
			io.WriteString(w, `{"ok":true,"result":[{"update_id":7,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/status"}}]}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var p map[string]any
			json.Unmarshal(body, &p)
			if p["parse_mode"] != "HTML" || p["reply_markup"] == nil {
				t.Errorf("bad send params: %s", body)
			}
			io.WriteString(w, `{"ok":true,"result":{"message_id":99,"chat":{"id":42}}}`)
		}
	}))
	defer srv.Close()
	c := New("SECRET", srv.URL, nil)
	ups, err := c.GetUpdates(context.Background(), 0, time.Second)
	if err != nil || len(ups) != 1 || ups[0].Message.From.ID != 42 || ups[0].Message.Text != "/status" {
		t.Fatalf("updates: %v %+v", err, ups)
	}
	id, err := c.SendMessage(context.Background(), 42, "hi", &InlineKeyboard{Rows: [][]Button{{{Text: "a", CallbackData: "b"}}}})
	if err != nil || id != 99 {
		t.Fatalf("send: %v %d", err, id)
	}
	if gotPaths[0] != "/botSECRET/getUpdates" {
		t.Fatalf("path: %v", gotPaths)
	}
}

func TestAPIErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: nope"}`)
	}))
	c := New("SECRET", srv.URL, nil)
	err := c.AnswerCallback(context.Background(), "1", "x")
	if err == nil || !strings.Contains(err.Error(), "Bad Request") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err: %v", err)
	}
	srv.Close()
	err = c.AnswerCallback(context.Background(), "1", "x") // connection refused
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("transport error leaks token: %v", err)
	}
}

func TestSendDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		f, h, err := r.FormFile("document")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		if h.Filename != "a.txt" || string(b) != "data" || r.FormValue("chat_id") != "5" {
			t.Errorf("bad form: %s %s %s", h.Filename, b, r.FormValue("chat_id"))
		}
		io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	if err := New("t", srv.URL, nil).SendDocument(context.Background(), 5, "a.txt", []byte("data"), "cap"); err != nil {
		t.Fatal(err)
	}
}
