// Package config loads service settings from environment variables. The list
// of destinations is not here: it lives in the file named by NOTIFY_CONFIG
// (see package destinations) because it holds personal data.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/sanitize"
)

// Limits are the per-destination defaults; a destination may override them.
type Limits struct {
	MaxTextChars  int // characters of agent text, before the header
	RatePerMinute int
	RatePerHour   int
}

// Ceiling for MaxTextChars (also enforced on per-destination overrides).
const MaxTextCharsCeiling = 20000

// DefaultMaxRequestBytes bounds one MCP request body: a message is at most a
// few tens of KB, and the SDK default (4 MiB) would let junk calls pile up in
// a small pod.
const DefaultMaxRequestBytes = 64 << 10

// Config holds all settings.
type Config struct {
	Addr           string
	LogLevel       string
	ConfigPath     string
	RequestTimeout time.Duration
	ShutdownGrace  time.Duration

	OutboundTimeout time.Duration // per upstream HTTP call
	MaxRetryAfter   time.Duration // longest upstream 429 wait we will sit through once
	MaxInFlight     int           // concurrent sends across all destinations
	Limits          Limits
	SenderLabel     string

	IdempotencyWindow     time.Duration
	IdempotencyMaxEntries int

	// Caps across all destinations together; a multi-part Telegram message
	// consumes one token per part.
	GlobalRatePerMinute int
	GlobalRatePerHour   int

	MCPStateless bool

	// MaxRequestBytes bounds one MCP HTTP request body.
	MaxRequestBytes int64

	// MetricsAddr, when set, is the listen address of the separate Prometheus
	// listener (GET /metrics only, no authentication). Empty means no metrics.
	MetricsAddr string
}

// Load reads configuration using getenv (e.g. os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:                  orDefault(getenv("NOTIFY_ADDR"), ":8080"),
		LogLevel:              orDefault(getenv("NOTIFY_LOG_LEVEL"), "info"),
		ConfigPath:            getenv("NOTIFY_CONFIG"),
		RequestTimeout:        30 * time.Second,
		ShutdownGrace:         25 * time.Second,
		OutboundTimeout:       10 * time.Second,
		MaxRetryAfter:         10 * time.Second,
		MaxInFlight:           8,
		Limits:                Limits{MaxTextChars: 4000, RatePerMinute: 6, RatePerHour: 60},
		SenderLabel:           "OpenAgentix agent",
		IdempotencyWindow:     10 * time.Minute,
		IdempotencyMaxEntries: 1000,
		GlobalRatePerMinute:   30,
		GlobalRatePerHour:     300,
		MCPStateless:          true,
		MaxRequestBytes:       DefaultMaxRequestBytes,
	}
	if v := getenv("NOTIFY_SENDER_LABEL"); v != "" {
		c.SenderLabel = v
	}
	var err error
	durs := []struct {
		name string
		dst  *time.Duration
		zero bool
	}{
		{"NOTIFY_REQUEST_TIMEOUT", &c.RequestTimeout, false},
		{"NOTIFY_SHUTDOWN_GRACE", &c.ShutdownGrace, true},
		{"NOTIFY_OUTBOUND_TIMEOUT", &c.OutboundTimeout, false},
		{"NOTIFY_MAX_RETRY_AFTER", &c.MaxRetryAfter, true},
		{"NOTIFY_IDEMPOTENCY_WINDOW", &c.IdempotencyWindow, false},
	}
	for _, d := range durs {
		if *d.dst, err = duration(getenv, d.name, *d.dst, d.zero); err != nil {
			return c, err
		}
	}
	ints := []struct {
		name string
		dst  *int
	}{
		{"NOTIFY_MAX_INFLIGHT", &c.MaxInFlight},
		{"NOTIFY_MAX_TEXT_CHARS", &c.Limits.MaxTextChars},
		{"NOTIFY_RATE_PER_MINUTE", &c.Limits.RatePerMinute},
		{"NOTIFY_RATE_PER_HOUR", &c.Limits.RatePerHour},
		{"NOTIFY_IDEMPOTENCY_MAX_ENTRIES", &c.IdempotencyMaxEntries},
		{"NOTIFY_GLOBAL_RATE_PER_MINUTE", &c.GlobalRatePerMinute},
		{"NOTIFY_GLOBAL_RATE_PER_HOUR", &c.GlobalRatePerHour},
	}
	for _, i := range ints {
		if *i.dst, err = positiveInt(getenv, i.name, *i.dst); err != nil {
			return c, err
		}
	}
	reqBytes := int(c.MaxRequestBytes)
	if reqBytes, err = positiveInt(getenv, "NOTIFY_MAX_REQUEST_BYTES", reqBytes); err != nil {
		return c, err
	}
	c.MaxRequestBytes = int64(reqBytes)
	if c.Limits.MaxTextChars > MaxTextCharsCeiling {
		return c, fmt.Errorf("NOTIFY_MAX_TEXT_CHARS: %d exceeds the maximum of %d", c.Limits.MaxTextChars, MaxTextCharsCeiling)
	}
	c.SenderLabel = sanitize.Line(c.SenderLabel)
	if c.SenderLabel == "" || len([]rune(c.SenderLabel)) > 60 {
		return c, fmt.Errorf("NOTIFY_SENDER_LABEL: must be 1 to 60 printable characters")
	}
	switch v := getenv("NOTIFY_MCP_STATELESS"); v {
	case "", "true":
	case "false":
		c.MCPStateless = false
	default:
		return c, fmt.Errorf("NOTIFY_MCP_STATELESS: must be true or false, got %q", v)
	}
	if c.MetricsAddr = getenv("NOTIFY_METRICS_ADDR"); c.MetricsAddr != "" {
		_, port, perr := net.SplitHostPort(c.MetricsAddr)
		if perr != nil || port == "" {
			return c, fmt.Errorf("NOTIFY_METRICS_ADDR: %q must be host:port or :port", c.MetricsAddr)
		}
		if c.MetricsAddr == c.Addr && port != "0" { // port 0 picks a free port each, so two of them never collide
			return c, fmt.Errorf("NOTIFY_METRICS_ADDR: %q must differ from NOTIFY_ADDR (metrics are served on their own listener)", c.MetricsAddr)
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, fmt.Errorf("NOTIFY_LOG_LEVEL: invalid level %q", c.LogLevel)
	}
	return c, nil
}

// LogAttrs describes the configuration for the startup log. Nothing here is
// secret: the destinations file is never echoed.
func (c Config) LogAttrs() []any {
	return []any{
		"addr", c.Addr, "config_file_set", c.ConfigPath != "", "request_timeout", c.RequestTimeout.String(),
		"outbound_timeout", c.OutboundTimeout.String(), "max_inflight", c.MaxInFlight,
		"max_text_chars", c.Limits.MaxTextChars, "rate_per_minute", c.Limits.RatePerMinute, "rate_per_hour", c.Limits.RatePerHour, "global_rate_per_minute", c.GlobalRatePerMinute, "global_rate_per_hour", c.GlobalRatePerHour,
		"sender_label", c.SenderLabel, "idempotency_window", c.IdempotencyWindow.String(), "mcp_stateless", c.MCPStateless, "max_request_bytes", c.MaxRequestBytes, "metrics_addr", c.MetricsAddr,
	}
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func positiveInt(getenv func(string) string, name string, def int) (int, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: invalid value %q", name, v)
	}
	return n, nil
}

func duration(getenv func(string) string, name string, def time.Duration, allowZero bool) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 || (d == 0 && !allowZero) {
		return 0, fmt.Errorf("%s: invalid duration %q", name, v)
	}
	return d, nil
}
