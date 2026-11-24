// Package config loads the gate's settings from environment variables. The per-caller rules are not here: they are
// in the file named by A2A_GATE_RULES (package rules).
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// Config holds all settings.
type Config struct {
	Addr        string
	Upstream    string
	Audience    string
	RulesPath   string
	KeysDir     string
	AuditUserID string // hash | raw
	SniffBytes  int
	MaxBody     int64
	LogLevel    string
}

// MaxSniffBytes is the ceiling of A2A_GATE_SNIFF_BYTES: the gate must never buffer a large part of a request.
const MaxSniffBytes = 1 << 20

// Load reads configuration using getenv (e.g. os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:        or(getenv("A2A_GATE_ADDR"), ":8080"),
		Upstream:    getenv("A2A_GATE_UPSTREAM"),
		Audience:    or(getenv("A2A_GATE_AUDIENCE"), "openagentix-a2a"),
		RulesPath:   getenv("A2A_GATE_RULES"),
		KeysDir:     or(getenv("A2A_GATE_KEYS_DIR"), "/etc/a2a-gate/keys"),
		AuditUserID: or(getenv("A2A_GATE_AUDIT_USER_ID"), "hash"),
		SniffBytes:  64 << 10,
		MaxBody:     8 << 20,
		LogLevel:    or(getenv("A2A_GATE_LOG_LEVEL"), "info"),
	}
	u, err := url.Parse(c.Upstream)
	if c.Upstream == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, errors.New("A2A_GATE_UPSTREAM must be an http or https URL")
	}
	if c.RulesPath == "" {
		return c, errors.New("A2A_GATE_RULES is required")
	}
	if c.AuditUserID != "hash" && c.AuditUserID != "raw" {
		return c, fmt.Errorf("A2A_GATE_AUDIT_USER_ID must be hash or raw, got %q", c.AuditUserID)
	}
	if v := getenv("A2A_GATE_SNIFF_BYTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > MaxSniffBytes {
			return c, fmt.Errorf("A2A_GATE_SNIFF_BYTES must be 1..%d", MaxSniffBytes)
		}
		c.SniffBytes = n
	}
	if v := getenv("A2A_GATE_MAX_BODY"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return c, errors.New("A2A_GATE_MAX_BODY must be a positive number of bytes")
		}
		c.MaxBody = n
	}
	return c, nil
}

func or(v, d string) string {
	if v != "" {
		return v
	}
	return d
}
