package mcpserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/notify/internal/obs"
)

func TestToolCallAndSendMetricsNeverCarryContent(t *testing.T) {
	m := obs.New("test")
	setupMetrics = m
	defer func() { setupMetrics = nil }()
	e := setup(t, true, 0, destYAML)

	e.mustCall(t, "notify_list_destinations", map[string]any{})
	e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": "canary-message-text delivered"})
	e.mustCall(t, "notify_send", map[string]any{"destination": "no-such-canary-dest", "text": "canary-message-text refused"})
	e.mustCall(t, "notify_send", map[string]any{"destination": "ops", "text": strings.Repeat("canary-long ", 20)}) // over this destination's 50 chars

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		`openagentix_notify_mcp_tool_calls_total{tool="notify_list_destinations",outcome="ok"} 1`,
		`openagentix_notify_mcp_tool_calls_total{tool="notify_send",outcome="ok"} 1`,
		`openagentix_notify_mcp_tool_calls_total{tool="notify_send",outcome="unknown_destination"} 1`,
		`openagentix_notify_mcp_tool_calls_total{tool="notify_send",outcome="too_long"} 1`,
		`openagentix_notify_sends_total{kind="telegram",outcome="delivered"} 1`,
		`openagentix_notify_sends_total{kind="unknown",outcome="unknown_destination"} 1`,
		`openagentix_notify_payload_rejections_total{kind="telegram",field="text"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, canary := range []string{"canary", tgToken, tgChat, slToken, slChannel, hookURL, "PRIVATE", "ops", "audit", "team"} {
		if strings.Contains(out, canary) {
			t.Errorf("forbidden %q in /metrics", canary)
		}
	}
}
