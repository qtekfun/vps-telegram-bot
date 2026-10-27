// Command vps-telegram-bot lets an allowlisted Telegram user inspect and
// operate the Docker containers of one VPS.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/qtekfun/vps-telegram-bot/internal/bot"
	"github.com/qtekfun/vps-telegram-bot/internal/config"
	"github.com/qtekfun/vps-telegram-bot/internal/docker"
	"github.com/qtekfun/vps-telegram-bot/internal/gatus"
	"github.com/qtekfun/vps-telegram-bot/internal/hoststat"
	"github.com/qtekfun/vps-telegram-bot/internal/telegram"
)

const pollTimeout = 50 * time.Second

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.OS()
	if err != nil {
		return err
	}
	dk, err := docker.New(cfg.DockerHost, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	var checks bot.Checks
	if cfg.GatusURL != "" {
		checks = gatus.New(cfg.GatusURL, &http.Client{Timeout: 15 * time.Second})
	}
	tg := telegram.New(cfg.Token, "", nil)
	b := bot.New(tg, dk, checks, bot.Options{
		Allowed:          cfg.AllowedUserIDs,
		ActionContainers: cfg.ActionContainers,
		ConfirmTTL:       cfg.ConfirmTTL,
		Host:             func() hoststat.Info { return hoststat.Read("/proc", cfg.DiskPaths) },
		Alerts:           func() (map[string]string, error) { return hoststat.Alerts(cfg.MonitorStatePath) },
		Log:              log,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := tg.SetMyCommands(ctx, bot.Commands()); err != nil {
		log.Warn("setMyCommands failed", "err", err)
	}
	log.Info("started", "allowed_users", len(cfg.AllowedUserIDs), "action_containers", len(cfg.ActionContainers), "gatus", cfg.GatusURL != "")

	offset, backoff := 0, time.Second
	for ctx.Err() == nil {
		ups, err := tg.GetUpdates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Warn("polling failed", "err", err, "retry_in", backoff)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, u := range ups {
			offset = max(offset, u.UpdateID+1)
			b.Handle(ctx, u)
		}
	}
	log.Info("stopped")
	return nil
}
