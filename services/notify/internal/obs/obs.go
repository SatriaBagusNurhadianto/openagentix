// Package obs holds the notify service's Prometheus metrics. Every method is
// safe on a nil *Metrics (metrics off) and takes only values from closed sets:
// anything else is folded into a fixed value, so no message text, destination
// name or address, chat id or token can become a label.
//
// Destination NAMES are deliberately not labels. They are operator-chosen
// strings (a name such as "owner-telegram" can identify a person) and only the
// platform KIND is a closed set of three. Per-destination detail stays in the
// service log, which already carries the name.
package obs

import (
	"net/http"
	"slices"
	"time"

	"github.com/openagentix/openagentix/services/notify/internal/metrics"
)

// Prefix of every metric this service defines.
const Prefix = "openagentix_notify"

var (
	kinds        = []string{"telegram", "slack", "webhook", "unknown"}
	sendOutcomes = []string{"delivered", "duplicate", "rejected_validation", "rate_limited", "unknown_destination", "upstream_error", "busy", "uncertain"}
	scopes       = []string{"destination", "global"}
	fields       = []string{"text", "title", "destination", "idempotency_key"}
	tools        = []string{"notify_send", "notify_list_destinations"}
	toolOutcomes = []string{"ok", "invalid", "unknown_destination", "too_long", "rate_limited", "busy", "upstream", "timeout", "disabled", "uncertain", "error"}
)

func pick(v, fallback string, allowed []string) string {
	if slices.Contains(allowed, v) {
		return v
	}
	return fallback
}

// Metrics is the set of series. Create it with New.
type Metrics struct {
	reg      *metrics.Registry
	sends    *metrics.CounterVec
	sendDur  *metrics.HistogramVec
	limited  *metrics.CounterVec
	payload  *metrics.CounterVec
	toolCall *metrics.CounterVec
	toolDur  *metrics.HistogramVec
}

// New registers every series and the process basics.
func New(version string) *Metrics {
	r := metrics.NewRegistry()
	m := &Metrics{reg: r}
	m.sends = r.Counter(Prefix+"_sends_total", "Send requests by destination platform kind and outcome.", "kind", "outcome")
	m.sendDur = r.Histogram(Prefix+"_send_duration_seconds", "Send request duration by destination platform kind.", metrics.DefBuckets, "kind")
	m.limited = r.Counter(Prefix+"_rate_limit_rejections_total", "Sends refused by this service's own rate limits, by scope (destination or global).", "scope")
	m.payload = r.Counter(Prefix+"_payload_rejections_total", "Sends refused because a field was too long, by platform kind and field.", "kind", "field")
	m.toolCall = r.Counter(Prefix+"_mcp_tool_calls_total", "MCP tool calls by tool and outcome.", "tool", "outcome")
	m.toolDur = r.Histogram(Prefix+"_mcp_tool_call_duration_seconds", "MCP tool call duration by tool.", metrics.DefBuckets, "tool")
	metrics.RegisterRuntime(r, Prefix, version)
	return m
}

// Handler serves GET /metrics (and nothing else).
func (m *Metrics) Handler() http.Handler { return m.reg.Handler() }

// Send records one send request. kind is the platform of a known destination,
// "unknown" otherwise. outcome is one of the values in sendOutcomes.
func (m *Metrics) Send(kind, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	kind = pick(kind, "unknown", kinds)
	m.sends.With(kind, pick(outcome, "error", append(sendOutcomes, "error"))).Inc()
	m.sendDur.With(kind).Observe(d.Seconds())
}

// RateLimited records a send refused by a local limit ("destination" or "global").
func (m *Metrics) RateLimited(scope string) {
	if m != nil {
		m.limited.With(pick(scope, "other", append(scopes, "other"))).Inc()
	}
}

// PayloadRejected records a send refused for size; field names the field.
func (m *Metrics) PayloadRejected(kind, field string) {
	if m != nil {
		m.payload.With(pick(kind, "unknown", kinds), pick(field, "other", append(fields, "other"))).Inc()
	}
}

// ToolCall records one MCP tool call.
func (m *Metrics) ToolCall(tool, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	tool = pick(tool, "other", tools)
	m.toolCall.With(tool, pick(outcome, "error", toolOutcomes)).Inc()
	m.toolDur.With(tool).Observe(d.Seconds())
}
