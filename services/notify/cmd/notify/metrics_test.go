package main

import (
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMetricsListenerOffByDefault(t *testing.T) {
	called := false
	onMetricsListen = func(string) { called = true }
	defer func() { onMetricsListen = nil }()
	r := start(t, map[string]string{})
	if code, _ := status(t, r.addr, "/metrics"); code != 404 {
		t.Fatalf("API listener serves /metrics: %d", code)
	}
	if called {
		t.Fatal("metrics listener started without NOTIFY_METRICS_ADDR")
	}
}

func TestMetricsListenerEndToEndAndPrivacy(t *testing.T) {
	addr := make(chan string, 1)
	onMetricsListen = func(a string) { addr <- a }
	defer func() { onMetricsListen = nil }()
	r := start(t, map[string]string{"NOTIFY_CONFIG": writeConfig(t, cfgYAML), "TG_TOKEN": "111:FAKE-TOKEN", "NOTIFY_METRICS_ADDR": "127.0.0.1:0"})
	var maddr string
	select {
	case maddr = <-addr:
	case <-time.After(5 * time.Second):
		t.Fatal("metrics listener did not start")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: "http://" + r.addr + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	_, _ = sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "notify_send", Arguments: map[string]any{"destination": "canary-nowhere", "text": "canary-message-text"}})
	_, _ = sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "notify_list_destinations"})

	code, out := status(t, maddr, "/metrics")
	if code != 200 {
		t.Fatalf("metrics %d", code)
	}
	for _, want := range []string{
		`openagentix_notify_sends_total{kind="unknown",outcome="unknown_destination"} 1`,
		`openagentix_notify_mcp_tool_calls_total{tool="notify_send",outcome="unknown_destination"} 1`,
		`openagentix_notify_mcp_tool_calls_total{tool="notify_list_destinations",outcome="ok"} 1`,
		`openagentix_notify_build_info{version="dev"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, canary := range []string{"canary", "FAKE-TOKEN", "1002223334444", "ops"} {
		if strings.Contains(out, canary) {
			t.Errorf("forbidden %q in /metrics", canary)
		}
	}
	for _, path := range []string{"/mcp", "/healthz", "/readyz"} {
		if code, _ := status(t, maddr, path); code != 404 {
			t.Errorf("metrics listener serves %s: %d", path, code)
		}
	}
	if code, _ := status(t, r.addr, "/metrics"); code != 404 {
		t.Errorf("API listener serves /metrics: %d", code)
	}
	for _, canary := range []string{"canary-message-text", "FAKE-TOKEN", "1002223334444"} {
		if strings.Contains(r.logs.String(), canary) {
			t.Errorf("forbidden %q in logs", canary)
		}
	}
}
