// Package config loads bridge settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BotToken           string
	A2AURL             string
	A2AToken           string
	APIBase            string
	AgentTimeout       time.Duration
	AllowedChatIDs     map[int64]bool
	AllowAll           bool // explicit opt-in to accept every chat
	MaxConcurrentTurns int
	ShutdownGrace      time.Duration
	HealthAddr         string
}

// Load reads configuration using getenv (e.g. os.Getenv).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		BotToken:           getenv("TELEGRAM_BOT_TOKEN"),
		A2AURL:             getenv("A2A_URL"),
		A2AToken:           getenv("A2A_TOKEN"),
		APIBase:            getenv("TELEGRAM_API_BASE"),
		AgentTimeout:       120 * time.Second,
		AllowedChatIDs:     map[int64]bool{},
		MaxConcurrentTurns: 8,
		ShutdownGrace:      25 * time.Second,
		HealthAddr:         getenv("HEALTH_ADDR"),
	}
	if c.BotToken == "" {
		return c, errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	if c.A2AURL == "" {
		return c, errors.New("A2A_URL is required")
	}
	if c.HealthAddr == "" {
		c.HealthAddr = ":8080"
	}
	if c.APIBase == "" {
		c.APIBase = "https://api.telegram.org"
	}
	if v := getenv("AGENT_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return c, fmt.Errorf("AGENT_TIMEOUT: invalid duration %q", v)
		}
		c.AgentTimeout = d
	}
	if v := getenv("MAX_CONCURRENT_TURNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return c, fmt.Errorf("MAX_CONCURRENT_TURNS: invalid count %q", v)
		}
		c.MaxConcurrentTurns = n
	}
	if v := getenv("SHUTDOWN_GRACE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return c, fmt.Errorf("SHUTDOWN_GRACE: invalid duration %q", v)
		}
		c.ShutdownGrace = d
	}
	if v := getenv("TELEGRAM_ALLOWED_CHAT_IDS"); strings.TrimSpace(v) != "" {
		for _, part := range strings.Split(v, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil {
				return c, fmt.Errorf("TELEGRAM_ALLOWED_CHAT_IDS: invalid chat id %q", part)
			}
			c.AllowedChatIDs[id] = true
		}
	}
	switch v := getenv("TELEGRAM_ALLOW_ALL"); v {
	case "", "false":
	case "true":
		c.AllowAll = true
	default:
		return c, fmt.Errorf("TELEGRAM_ALLOW_ALL: must be true or false, got %q", v)
	}
	if len(c.AllowedChatIDs) == 0 && !c.AllowAll {
		return c, errors.New("access control: set TELEGRAM_ALLOWED_CHAT_IDS to a comma-separated list of chat ids, or TELEGRAM_ALLOW_ALL=true to accept every chat")
	}
	return c, nil
}
