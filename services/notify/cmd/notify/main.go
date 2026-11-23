// Command notify serves the OpenAgentix notify service: an MCP endpoint that
// lets agents deliver a result to a named, operator-approved chat destination.
//
// The MCP endpoint is unauthenticated by design. The tool gateway in front of
// it authenticates agents; deploy/networkpolicy.yaml admits only that gateway.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/openagentix/openagentix/services/notify/internal/config"
	"github.com/openagentix/openagentix/services/notify/internal/destinations"
	"github.com/openagentix/openagentix/services/notify/internal/httpx"
	"github.com/openagentix/openagentix/services/notify/internal/notify"
	"github.com/openagentix/openagentix/services/notify/internal/server"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	level := slog.LevelInfo
	if v := os.Getenv("NOTIFY_LOG_LEVEL"); v != "" {
		_ = level.UnmarshalText([]byte(v))
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, os.Getenv, log, nil); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("notify stopped", "err", err)
		os.Exit(1)
	}
	log.Info("notify stopped")
}

// run wires everything and serves until ctx is cancelled. onListen, if set,
// receives the bound address once the listener is up.
func run(ctx context.Context, getenv func(string) string, log *slog.Logger, onListen func(addr string)) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	ds, err := destinations.Load(cfg.ConfigPath, destinations.Options{
		Defaults: cfg.Limits, HTTP: httpx.NewClient(cfg.OutboundTimeout), MaxRetryAfter: cfg.MaxRetryAfter, Getenv: getenv,
	})
	switch {
	case errors.Is(err, destinations.ErrNotConfigured):
		log.Warn("no destinations file found: starting with no destinations, every send will be refused and /readyz will report not ready", "config_file_set", cfg.ConfigPath != "")
		ds = nil
	case err != nil:
		return fmt.Errorf("invalid destinations file: %w", err)
	case len(ds) == 0:
		log.Warn("the destinations file defines no destinations: every send will be refused and /readyz will report not ready")
	}
	svc := notify.New(notify.Options{
		Destinations: ds, Label: cfg.SenderLabel, MaxInFlight: cfg.MaxInFlight,
		IdempotencyWindow: cfg.IdempotencyWindow, IdempotencyMaxEntries: cfg.IdempotencyMaxEntries, Log: log,
		GlobalRatePerMinute: cfg.GlobalRatePerMinute, GlobalRatePerHour: cfg.GlobalRatePerHour,
	})
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Info("notify starting", append(cfg.LogAttrs(), "version", version, "listen", ln.Addr().String(), "destinations", len(ds))...)
	for _, d := range svc.List() {
		log.Info("destination enabled", "destination", d.Name, "platform", d.Platform, "max_text_chars", d.MaxTextChars)
	}
	if onListen != nil {
		onListen(ln.Addr().String())
	}
	return server.Run(ctx, server.Deps{Svc: svc, Cfg: cfg, Log: log, Version: version}, ln)
}
