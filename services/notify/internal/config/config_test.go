package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.ConfigPath != "" || c.RequestTimeout != 30*time.Second || c.ShutdownGrace != 25*time.Second ||
		c.OutboundTimeout != 10*time.Second || c.MaxRetryAfter != 10*time.Second || c.MaxInFlight != 8 ||
		c.Limits.MaxTextChars != 4000 || c.Limits.RatePerMinute != 6 || c.Limits.RatePerHour != 60 ||
		c.SenderLabel != "OpenAgentix agent" || c.IdempotencyWindow != 10*time.Minute || c.IdempotencyMaxEntries != 1000 ||
		!c.MCPStateless || c.LogLevel != "info" {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"NOTIFY_ADDR": ":9000", "NOTIFY_CONFIG": "/etc/notify/destinations.yaml", "NOTIFY_REQUEST_TIMEOUT": "5s",
		"NOTIFY_SHUTDOWN_GRACE": "1s", "NOTIFY_OUTBOUND_TIMEOUT": "2s", "NOTIFY_MAX_RETRY_AFTER": "3s",
		"NOTIFY_MAX_INFLIGHT": "2", "NOTIFY_MAX_TEXT_CHARS": "500", "NOTIFY_RATE_PER_MINUTE": "2", "NOTIFY_RATE_PER_HOUR": "20",
		"NOTIFY_SENDER_LABEL": "Test Bot", "NOTIFY_IDEMPOTENCY_WINDOW": "1m", "NOTIFY_IDEMPOTENCY_MAX_ENTRIES": "5",
		"NOTIFY_MCP_STATELESS": "false", "NOTIFY_LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":9000" || c.ConfigPath != "/etc/notify/destinations.yaml" || c.RequestTimeout != 5*time.Second ||
		c.ShutdownGrace != time.Second || c.OutboundTimeout != 2*time.Second || c.MaxRetryAfter != 3*time.Second ||
		c.MaxInFlight != 2 || c.Limits.MaxTextChars != 500 || c.Limits.RatePerMinute != 2 || c.Limits.RatePerHour != 20 ||
		c.SenderLabel != "Test Bot" || c.IdempotencyWindow != time.Minute || c.IdempotencyMaxEntries != 5 ||
		c.MCPStateless || c.LogLevel != "debug" {
		t.Fatalf("%+v", c)
	}
}

func TestInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"NOTIFY_REQUEST_TIMEOUT":         {"NOTIFY_REQUEST_TIMEOUT": "soon"},
		"NOTIFY_OUTBOUND_TIMEOUT":        {"NOTIFY_OUTBOUND_TIMEOUT": "0s"},
		"NOTIFY_MAX_INFLIGHT":            {"NOTIFY_MAX_INFLIGHT": "0"},
		"NOTIFY_MAX_TEXT_CHARS":          {"NOTIFY_MAX_TEXT_CHARS": "-1"},
		"NOTIFY_MAX_TEXT_CHARS ceiling":  {"NOTIFY_MAX_TEXT_CHARS": "1000000"},
		"NOTIFY_RATE_PER_MINUTE":         {"NOTIFY_RATE_PER_MINUTE": "x"},
		"NOTIFY_MCP_STATELESS":           {"NOTIFY_MCP_STATELESS": "maybe"},
		"NOTIFY_LOG_LEVEL":               {"NOTIFY_LOG_LEVEL": "loud"},
		"NOTIFY_SENDER_LABEL":            {"NOTIFY_SENDER_LABEL": "   "},
		"NOTIFY_IDEMPOTENCY_MAX_ENTRIES": {"NOTIFY_IDEMPOTENCY_MAX_ENTRIES": "0"},
		"NOTIFY_SENDER_LABEL long":       {"NOTIFY_SENDER_LABEL": strings.Repeat("a", 100)},
	}
	for name, m := range cases {
		_, err := Load(env(m))
		if err == nil {
			t.Errorf("%s: want error", name)
			continue
		}
		if !strings.Contains(err.Error(), strings.Fields(name)[0]) {
			t.Errorf("%s: error %q should name the variable", name, err)
		}
	}
}

func TestLogAttrsOmitConfigPathContents(t *testing.T) {
	c, _ := Load(env(map[string]string{"NOTIFY_CONFIG": "/etc/notify/destinations.yaml"}))
	if len(c.LogAttrs())%2 != 0 {
		t.Fatal("LogAttrs must be key/value pairs")
	}
}

func TestLoad_GlobalRate(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil || c.GlobalRatePerMinute != 30 || c.GlobalRatePerHour != 300 {
		t.Fatalf("defaults: %v %+v", err, c)
	}
	c, err = Load(env(map[string]string{"NOTIFY_GLOBAL_RATE_PER_MINUTE": "5", "NOTIFY_GLOBAL_RATE_PER_HOUR": "50"}))
	if err != nil || c.GlobalRatePerMinute != 5 || c.GlobalRatePerHour != 50 {
		t.Fatalf("override: %v %+v", err, c)
	}
	if _, err = Load(env(map[string]string{"NOTIFY_GLOBAL_RATE_PER_MINUTE": "0"})); err == nil {
		t.Fatal("zero must be rejected")
	}
}

func TestLoad_MaxRequestBytes(t *testing.T) {
	c, err := Load(env(map[string]string{}))
	if err != nil || c.MaxRequestBytes != 64<<10 {
		t.Fatalf("default: %v %d", err, c.MaxRequestBytes)
	}
	c, err = Load(env(map[string]string{"NOTIFY_MAX_REQUEST_BYTES": "4096"}))
	if err != nil || c.MaxRequestBytes != 4096 {
		t.Fatalf("override: %v %d", err, c.MaxRequestBytes)
	}
	if _, err = Load(env(map[string]string{"NOTIFY_MAX_REQUEST_BYTES": "-1"})); err == nil {
		t.Fatal("negative must be rejected")
	}
}

func TestMetricsConfig(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil || c.MetricsAddr != "" {
		t.Fatalf("metrics must be off by default: %+v %v", c, err)
	}
	if c, err = Load(env(map[string]string{"NOTIFY_METRICS_ADDR": ":9090"})); err != nil || c.MetricsAddr != ":9090" {
		t.Fatalf("%+v %v", c, err)
	}
	for want, m := range map[string]map[string]string{
		"NOTIFY_METRICS_ADDR":          {"NOTIFY_METRICS_ADDR": "nine-thousand"},
		"must differ from NOTIFY_ADDR": {"NOTIFY_METRICS_ADDR": ":8080"},
	} {
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}
