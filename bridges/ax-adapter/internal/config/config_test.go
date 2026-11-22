package config

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.AXServer != "ax-server.ax-system.svc.cluster.local:8080" ||
		c.Turn.Atespace != "default" || c.Turn.Workspace != "assistant-ws" || c.Turn.Gateway != "anthropic-gw" ||
		c.Turn.TurnTimeout != 170*time.Second || c.Turn.PollInterval != 500*time.Millisecond ||
		c.Turn.KeepTasks || c.Turn.MaxHistoryTurns != 8 || c.Turn.ConfigBudgetBytes != 12288 ||
		c.Turn.MaxMessageBytes != 8192 || c.Turn.MaxConcurrentTasks != 2 || c.Turn.Image != "" {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestEnvOverrides(t *testing.T) {
	c, err := Load(nil, env(map[string]string{
		"LISTEN_ADDR": ":9", "AX_SERVER": "h:1", "ATESPACE": "x", "AX_WORKSPACE": "w", "AX_GATEWAY": "g",
		"AX_IMAGE": "i@sha256:1", "TURN_TIMEOUT": "5s", "POLL_INTERVAL": "10ms", "KEEP_TASKS": "true",
		"MAX_HISTORY_TURNS": "3", "CONFIG_BUDGET_BYTES": "999", "MAX_MESSAGE_BYTES": "77", "MAX_CONCURRENT_TASKS": "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":9" || c.AXServer != "h:1" || c.Turn.Atespace != "x" || c.Turn.Workspace != "w" ||
		c.Turn.Gateway != "g" || c.Turn.Image != "i@sha256:1" || c.Turn.TurnTimeout != 5*time.Second ||
		c.Turn.PollInterval != 10*time.Millisecond || !c.Turn.KeepTasks || c.Turn.MaxHistoryTurns != 3 ||
		c.Turn.ConfigBudgetBytes != 999 || c.Turn.MaxMessageBytes != 77 || c.Turn.MaxConcurrentTasks != 1 {
		t.Fatalf("env: %+v", c)
	}
}

func TestFlagBeatsEnv(t *testing.T) {
	c, err := Load([]string{"-turn-timeout", "9s", "-keep-tasks"}, env(map[string]string{"TURN_TIMEOUT": "5s"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Turn.TurnTimeout != 9*time.Second || !c.Turn.KeepTasks {
		t.Fatalf("%+v", c.Turn)
	}
}

func TestInvalidValues(t *testing.T) {
	for _, m := range []map[string]string{
		{"TURN_TIMEOUT": "soon"}, {"MAX_CONCURRENT_TASKS": "0"}, {"CONFIG_BUDGET_BYTES": "20000"}, {"MAX_HISTORY_TURNS": "x"},
	} {
		if _, err := Load(nil, env(m)); err == nil {
			t.Errorf("want error for %v", m)
		}
	}
}
