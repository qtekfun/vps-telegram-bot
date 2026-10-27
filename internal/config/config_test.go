package config

import (
	"errors"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func noFile(string) ([]byte, error) { return nil, errors.New("no file") }

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{
		"TELEGRAM_TOKEN":    " tok ",
		"ALLOWED_USER_IDS":  "1, 22",
		"ACTION_CONTAINERS": "traefik, ntfy,,",
		"GATUS_URL":         "http://gatus:8080/",
	}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "tok" || !c.AllowedUserIDs[1] || !c.AllowedUserIDs[22] || len(c.AllowedUserIDs) != 2 {
		t.Fatalf("bad config: %+v", c)
	}
	if c.DockerHost != "tcp://socket-proxy:2375" || c.GatusURL != "http://gatus:8080" {
		t.Fatalf("bad defaults: %+v", c)
	}
	if len(c.ActionContainers) != 2 || c.ConfirmTTL != 60*time.Second {
		t.Fatalf("bad lists/ttl: %+v", c)
	}
}

func TestTokenFileWins(t *testing.T) {
	c, err := Load(env(map[string]string{
		"TELEGRAM_TOKEN": "env", "TELEGRAM_TOKEN_FILE": "/run/secrets/t", "ALLOWED_USER_IDS": "5",
	}), func(string) ([]byte, error) { return []byte("file\n"), nil })
	if err != nil || c.Token != "file" {
		t.Fatalf("got %v %+v", err, c)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"no token":   {"ALLOWED_USER_IDS": "1"},
		"no users":   {"TELEGRAM_TOKEN": "t"},
		"bad user":   {"TELEGRAM_TOKEN": "t", "ALLOWED_USER_IDS": "abc"},
		"negative":   {"TELEGRAM_TOKEN": "t", "ALLOWED_USER_IDS": "-4"},
		"bad ttl":    {"TELEGRAM_TOKEN": "t", "ALLOWED_USER_IDS": "1", "CONFIRM_TTL": "1s"},
		"unreadable": {"TELEGRAM_TOKEN_FILE": "/x", "ALLOWED_USER_IDS": "1"},
	}
	for name, m := range cases {
		if _, err := Load(env(m), noFile); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
