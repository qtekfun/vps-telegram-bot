// Package config loads the bot configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the validated runtime configuration.
type Config struct {
	Token            string
	AllowedUserIDs   map[int64]bool
	DockerHost       string
	ActionContainers []string
	GatusURL         string
	DiskPaths        []string
	MonitorStatePath string
	ConfirmTTL       time.Duration
}

// Load reads the configuration using getenv (usually os.Getenv) and readFile
// (usually os.ReadFile) so it can be tested without touching the environment.
func Load(getenv func(string) string, readFile func(string) ([]byte, error)) (*Config, error) {
	c := &Config{
		DockerHost:       valueOr(getenv("DOCKER_HOST"), "tcp://socket-proxy:2375"),
		GatusURL:         strings.TrimRight(strings.TrimSpace(getenv("GATUS_URL")), "/"),
		MonitorStatePath: strings.TrimSpace(getenv("MONITOR_STATE_PATH")),
		ActionContainers: splitList(getenv("ACTION_CONTAINERS")),
		DiskPaths:        splitList(getenv("STATUS_DISK_PATHS")),
		ConfirmTTL:       60 * time.Second,
	}

	token, err := loadToken(getenv, readFile)
	if err != nil {
		return nil, err
	}
	c.Token = token

	ids, err := parseIDs(getenv("ALLOWED_USER_IDS"))
	if err != nil {
		return nil, err
	}
	c.AllowedUserIDs = ids

	if v := strings.TrimSpace(getenv("CONFIRM_TTL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 5*time.Second || d > 10*time.Minute {
			return nil, errors.New("CONFIRM_TTL must be a duration between 5s and 10m")
		}
		c.ConfirmTTL = d
	}
	return c, nil
}

func loadToken(getenv func(string) string, readFile func(string) ([]byte, error)) (string, error) {
	if path := strings.TrimSpace(getenv("TELEGRAM_TOKEN_FILE")); path != "" {
		b, err := readFile(path)
		if err != nil {
			return "", fmt.Errorf("reading TELEGRAM_TOKEN_FILE: %w", err)
		}
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
		return "", errors.New("TELEGRAM_TOKEN_FILE is empty")
	}
	if t := strings.TrimSpace(getenv("TELEGRAM_TOKEN")); t != "" {
		return t, nil
	}
	return "", errors.New("set TELEGRAM_TOKEN_FILE (preferred) or TELEGRAM_TOKEN")
}

func parseIDs(s string) (map[int64]bool, error) {
	ids := map[int64]bool{}
	for _, f := range splitList(s) {
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("ALLOWED_USER_IDS: %q is not a valid user id", f)
		}
		ids[id] = true
	}
	if len(ids) == 0 {
		return nil, errors.New("ALLOWED_USER_IDS must list at least one user id")
	}
	return ids, nil
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func valueOr(v, def string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return def
}

// OS is Load wired to the real environment and filesystem.
func OS() (*Config, error) { return Load(os.Getenv, os.ReadFile) }
