package a2a

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

// TraceContext is a W3C trace context (version 00, sampled). The bridge only STARTS a trace: it generates the ids and
// sends the traceparent header; it exports no spans of its own, so the first span the backend sees is the agent
// platform's, and its parent id points at a span that was never recorded.
type TraceContext struct{ TraceID, SpanID string }

var traceparentRe = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)

// NewTrace returns a fresh trace: random non-zero trace and span ids.
func NewTrace() TraceContext { return TraceContext{TraceID: randHex(16), SpanID: randHex(8)} }

// Child keeps the trace id and gets a new span id.
func (t TraceContext) Child() TraceContext {
	return TraceContext{TraceID: t.TraceID, SpanID: randHex(8)}
}

// Traceparent renders the header value: version-traceid-spanid-flags (flags 01 = sampled).
func (t TraceContext) Traceparent() string { return "00-" + t.TraceID + "-" + t.SpanID + "-01" }

// ParseTraceparent reads a version-00 header. All-zero ids and upper-case hex are invalid.
func ParseTraceparent(s string) (TraceContext, bool) {
	m := traceparentRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || m[1] == strings.Repeat("0", 32) || m[2] == strings.Repeat("0", 16) {
		return TraceContext{}, false
	}
	return TraceContext{TraceID: m[1], SpanID: m[2]}, true
}

type traceKey struct{}

// WithTrace returns a context whose trace Send forwards as the traceparent header.
func WithTrace(ctx context.Context, t TraceContext) context.Context {
	return context.WithValue(ctx, traceKey{}, t)
}

// TraceFromContext returns the trace set by WithTrace.
func TraceFromContext(ctx context.Context) (TraceContext, bool) {
	t, ok := ctx.Value(traceKey{}).(TraceContext)
	return t, ok
}

func randHex(n int) string {
	b := make([]byte, n)
	for {
		_, _ = rand.Read(b)
		for _, c := range b {
			if c != 0 {
				return hex.EncodeToString(b)
			}
		}
	}
}
