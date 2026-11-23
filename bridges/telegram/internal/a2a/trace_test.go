package a2a

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

var wantTraceparent = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-01$`)

func TestNewTrace_IsAValidW3CTraceparent(t *testing.T) {
	tc := NewTrace()
	m := wantTraceparent.FindStringSubmatch(tc.Traceparent())
	if m == nil {
		t.Fatalf("traceparent %q is not version-traceid-spanid-flags", tc.Traceparent())
	}
	if m[1] == strings.Repeat("0", 32) || m[2] == strings.Repeat("0", 16) {
		t.Fatalf("all-zero ids are invalid in W3C trace context: %q", tc.Traceparent())
	}
	if tc.TraceID != m[1] || tc.SpanID != m[2] {
		t.Fatalf("fields %+v do not match header %q", tc, tc.Traceparent())
	}
}

func TestNewTrace_IdsDiffer(t *testing.T) {
	a, b := NewTrace(), NewTrace()
	if a.TraceID == b.TraceID || a.SpanID == b.SpanID {
		t.Fatalf("two traces share ids: %+v %+v", a, b)
	}
}

func TestParseTraceparent(t *testing.T) {
	good := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tc, ok := ParseTraceparent(good)
	if !ok || tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || tc.SpanID != "00f067aa0ba902b7" {
		t.Fatalf("ParseTraceparent(%q) = %+v, %v", good, tc, ok)
	}
	for _, bad := range []string{"", "garbage", "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7"} {
		if _, ok := ParseTraceparent(bad); ok {
			t.Fatalf("ParseTraceparent(%q) accepted an invalid header", bad)
		}
	}
}

func TestChild_KeepsTraceIDNewSpan(t *testing.T) {
	p := NewTrace()
	c := p.Child()
	if c.TraceID != p.TraceID || c.SpanID == p.SpanID || !wantTraceparent.MatchString(c.Traceparent()) {
		t.Fatalf("child %+v of %+v", c, p)
	}
}

func TestTraceFromContext(t *testing.T) {
	if _, ok := TraceFromContext(context.Background()); ok {
		t.Fatal("empty context has a trace")
	}
	tc := NewTrace()
	got, ok := TraceFromContext(WithTrace(context.Background(), tc))
	if !ok || got != tc {
		t.Fatalf("got %+v, %v", got, ok)
	}
}

func TestSend_SendsTraceparentHeader(t *testing.T) {
	var cap captured
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"message":{"messageId":"m2","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"x"}]}}}`, &cap)
	defer srv.Close()
	if _, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi"); err != nil {
		t.Fatal(err)
	}
	if !wantTraceparent.MatchString(cap.header.Get("traceparent")) {
		t.Fatalf("traceparent header = %q", cap.header.Get("traceparent"))
	}
}

func TestSend_ForwardsTheTraceOfTheContext(t *testing.T) {
	var cap captured
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"message":{"messageId":"m2","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"x"}]}}}`, &cap)
	defer srv.Close()
	tc := NewTrace()
	if _, _, err := NewClient(srv.URL, "", srv.Client()).Send(WithTrace(context.Background(), tc), "", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := cap.header.Get("traceparent"); got != tc.Traceparent() {
		t.Fatalf("traceparent header = %q, want the context's %q", got, tc.Traceparent())
	}
}

func TestSend_NoBaggageHeader(t *testing.T) {
	var cap captured
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"message":{"messageId":"m2","contextId":"c","role":"ROLE_AGENT","parts":[{"text":"x"}]}}}`, &cap)
	defer srv.Close()
	ctx := WithUserID(WithTrace(context.Background(), NewTrace()), "telegram:42")
	if _, _, err := NewClient(srv.URL, "", srv.Client()).Send(ctx, "", "hi"); err != nil {
		t.Fatal(err)
	}
	if v := cap.header.Get("baggage"); v != "" {
		t.Fatalf("baggage header %q: ids and user ids must not ride in baggage", v)
	}
}
