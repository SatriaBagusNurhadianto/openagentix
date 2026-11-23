package obs

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(m *Metrics) string {
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestNilMetricsIsANoOp(t *testing.T) {
	var m *Metrics
	m.Send("telegram", "delivered", time.Second)
	m.RateLimited("destination")
	m.PayloadRejected("telegram", "text")
	m.ToolCall("notify_send", "ok", time.Second)
}

func TestEveryMetricAppearsWithItsLabels(t *testing.T) {
	m := New("v1.0.0")
	m.Send("telegram", "delivered", 40*time.Millisecond)
	m.Send("slack", "upstream_error", time.Second)
	m.Send("webhook", "duplicate", time.Millisecond)
	m.Send("unknown", "unknown_destination", time.Millisecond)
	m.RateLimited("global")
	m.PayloadRejected("webhook", "text")
	m.ToolCall("notify_send", "ok", time.Millisecond)
	m.ToolCall("notify_list_destinations", "ok", time.Millisecond)
	out := scrape(m)
	for _, want := range []string{
		`openagentix_notify_sends_total{kind="telegram",outcome="delivered"} 1`,
		`openagentix_notify_sends_total{kind="slack",outcome="upstream_error"} 1`,
		`openagentix_notify_sends_total{kind="webhook",outcome="duplicate"} 1`,
		`openagentix_notify_sends_total{kind="unknown",outcome="unknown_destination"} 1`,
		`openagentix_notify_send_duration_seconds_count{kind="telegram"} 1`,
		`openagentix_notify_rate_limit_rejections_total{scope="global"} 1`,
		`openagentix_notify_payload_rejections_total{kind="webhook",field="text"} 1`,
		`openagentix_notify_mcp_tool_calls_total{tool="notify_send",outcome="ok"} 1`,
		`openagentix_notify_mcp_tool_calls_total{tool="notify_list_destinations",outcome="ok"} 1`,
		`openagentix_notify_mcp_tool_call_duration_seconds_count{tool="notify_send"} 1`,
		`openagentix_notify_build_info{version="v1.0.0"} 1`,
		`go_goroutines `,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestUnknownValuesFoldToFixedOnes(t *testing.T) {
	m := New("dev")
	m.Send("owner-telegram", "oops-chat-123456", time.Millisecond)
	m.RateLimited("someone")
	m.PayloadRejected("x", "y")
	m.ToolCall("tool-with-secret", "weird", time.Millisecond)
	out := scrape(m)
	for _, bad := range []string{"owner-telegram", "123456", "someone", "secret", "weird", `kind="x"`} {
		if strings.Contains(out, bad) {
			t.Errorf("unbounded value %q reached the output", bad)
		}
	}
	for _, want := range []string{`kind="unknown",outcome="error"`, `scope="other"`, `kind="unknown",field="other"`, `tool="other",outcome="error"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
