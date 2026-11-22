// Package config reads adapter settings from environment variables, which
// command-line flags override.
package config

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/turn"
)

type Config struct {
	ListenAddr, AXServer string
	Turn                 turn.Config
}

// axMaxConfigBytes is AX's hard limit for spec.config.
const axMaxConfigBytes = 16 << 10

func Load(args []string, getenv func(string) string) (Config, error) {
	var perr error
	str := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	integer := func(k string, def int) int {
		v := getenv(k)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil && perr == nil {
			perr = fmt.Errorf("%s: %w", k, err)
		}
		return n
	}
	dur := func(k string, def time.Duration) time.Duration {
		v := getenv(k)
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil && perr == nil {
			perr = fmt.Errorf("%s: %w", k, err)
		}
		return d
	}
	boolean := func(k string) bool {
		v := getenv(k)
		if v == "" {
			return false
		}
		b, err := strconv.ParseBool(v)
		if err != nil && perr == nil {
			perr = fmt.Errorf("%s: %w", k, err)
		}
		return b
	}

	var c Config
	fs := flag.NewFlagSet("ax-adapter", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.ListenAddr, "listen-addr", str("LISTEN_ADDR", ":8080"), "HTTP listen address (LISTEN_ADDR)")
	fs.StringVar(&c.AXServer, "ax-server", str("AX_SERVER", "ax-server.ax-system.svc.cluster.local:8080"), "ax-server gRPC host:port (AX_SERVER)")
	fs.StringVar(&c.Turn.Atespace, "atespace", str("ATESPACE", "default"), "AX atespace (ATESPACE)")
	fs.StringVar(&c.Turn.Workspace, "workspace", str("AX_WORKSPACE", "assistant-ws"), "AX Workspace the tasks bind (AX_WORKSPACE)")
	fs.StringVar(&c.Turn.Gateway, "gateway", str("AX_GATEWAY", "anthropic-gw"), "AX Gateway the tasks bind; empty = none (AX_GATEWAY)")
	fs.StringVar(&c.Turn.Image, "image", str("AX_IMAGE", ""), "optional spec.profile.image override (AX_IMAGE)")
	fs.DurationVar(&c.Turn.TurnTimeout, "turn-timeout", dur("TURN_TIMEOUT", 170*time.Second), "per-turn timeout (TURN_TIMEOUT)")
	fs.DurationVar(&c.Turn.PollInterval, "poll-interval", dur("POLL_INTERVAL", 500*time.Millisecond), "GetTask poll interval (POLL_INTERVAL)")
	fs.BoolVar(&c.Turn.KeepTasks, "keep-tasks", boolean("KEEP_TASKS"), "do not delete finished tasks, for debugging (KEEP_TASKS)")
	fs.IntVar(&c.Turn.MaxHistoryTurns, "max-history-turns", integer("MAX_HISTORY_TURNS", 8), "user/assistant pairs kept per context (MAX_HISTORY_TURNS)")
	fs.IntVar(&c.Turn.ConfigBudgetBytes, "config-budget-bytes", integer("CONFIG_BUDGET_BYTES", 12288), "max JSON bytes of spec.config (CONFIG_BUDGET_BYTES)")
	fs.IntVar(&c.Turn.MaxMessageBytes, "max-message-bytes", integer("MAX_MESSAGE_BYTES", 8192), "max user message bytes (MAX_MESSAGE_BYTES)")
	fs.IntVar(&c.Turn.MaxConcurrentTasks, "max-concurrent-tasks", integer("MAX_CONCURRENT_TASKS", 2), "max simultaneous AX tasks (MAX_CONCURRENT_TASKS)")
	fs.IntVar(&c.Turn.MaxContexts, "max-contexts", integer("MAX_CONTEXTS", 1000), "max conversations kept in memory (MAX_CONTEXTS)")
	if perr != nil {
		return Config{}, perr
	}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	switch {
	case c.Turn.MaxConcurrentTasks < 1:
		return Config{}, fmt.Errorf("max-concurrent-tasks must be >= 1")
	case c.Turn.MaxHistoryTurns < 0 || c.Turn.MaxContexts < 1:
		return Config{}, fmt.Errorf("history limits must be positive")
	case c.Turn.ConfigBudgetBytes < 1 || c.Turn.ConfigBudgetBytes > axMaxConfigBytes:
		return Config{}, fmt.Errorf("config-budget-bytes must be 1..%d (AX limit)", axMaxConfigBytes)
	case c.Turn.TurnTimeout <= 0 || c.Turn.PollInterval <= 0:
		return Config{}, fmt.Errorf("timeouts must be positive")
	}
	return c, nil
}
