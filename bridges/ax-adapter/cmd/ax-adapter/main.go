// Command ax-adapter exposes the A2A 1.0 SendMessage surface and runs each
// turn as one Google AX Task.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/a2a"
	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/axbackend"
	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/config"
	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/turn"
)

func main() {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		slog.Error("bad configuration", "error", err)
		os.Exit(2)
	}
	be, err := axbackend.Dial(cfg.AXServer)
	if err != nil {
		slog.Error("cannot create AX client", "error", err)
		os.Exit(1)
	}
	defer be.Close()

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           a2a.NewHandler(turn.New(be, cfg.Turn)),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      cfg.Turn.TurnTimeout + 30*time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("ax-adapter listening", "addr", cfg.ListenAddr, "axServer", cfg.AXServer,
		"workspace", cfg.Turn.Workspace, "gateway", cfg.Turn.Gateway, "keepTasks", cfg.Turn.KeepTasks)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
