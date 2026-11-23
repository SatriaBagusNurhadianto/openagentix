package call

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

var wantTraceparent = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-01$`)

func TestRun_SendsAValidTraceparentAndReportsItsTraceID(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "ok", ""))
	res, err := Run(context.Background(), cfg(s.URL))
	if err != nil {
		t.Fatal(err)
	}
	h := c.headers[0].Get("traceparent")
	m := wantTraceparent.FindStringSubmatch(h)
	if m == nil {
		t.Fatalf("traceparent header %q", h)
	}
	if m[1] == strings.Repeat("0", 32) || m[2] == strings.Repeat("0", 16) {
		t.Fatalf("all-zero id in %q", h)
	}
	if res.TraceID != m[1] {
		t.Fatalf("Result.TraceID = %q, header trace id = %q", res.TraceID, m[1])
	}
}

func TestRun_EveryRunStartsItsOwnTrace(t *testing.T) {
	s := server(t, nil, 200, task("TASK_STATE_COMPLETED", "ok", ""))
	a, _ := Run(context.Background(), cfg(s.URL))
	b, _ := Run(context.Background(), cfg(s.URL))
	if a.TraceID == "" || a.TraceID == b.TraceID {
		t.Fatalf("trace ids %q %q", a.TraceID, b.TraceID)
	}
}

func TestRun_ContinuesAnIncomingTraceparentWithANewSpan(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "ok", ""))
	k := cfg(s.URL)
	k.Traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	res, err := Run(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	h := c.headers[0].Get("traceparent")
	m := wantTraceparent.FindStringSubmatch(h)
	if m == nil || m[1] != "4bf92f3577b34da6a3ce929d0e0e4736" || m[2] == "00f067aa0ba902b7" {
		t.Fatalf("header %q: want the incoming trace id and a new span id", h)
	}
	if res.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("TraceID = %q", res.TraceID)
	}
}

func TestRun_IgnoresAMalformedIncomingTraceparent(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "ok", ""))
	k := cfg(s.URL)
	k.Traceparent = "not-a-traceparent"
	if _, err := Run(context.Background(), k); err != nil {
		t.Fatal(err)
	}
	if !wantTraceparent.MatchString(c.headers[0].Get("traceparent")) {
		t.Fatalf("header %q", c.headers[0].Get("traceparent"))
	}
}

func TestRun_FailedRunsStillCarryTheTraceID(t *testing.T) {
	s := server(t, nil, 500, "boom")
	res, err := Run(context.Background(), cfg(s.URL))
	if err == nil || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(res.TraceID) {
		t.Fatalf("err = %v, TraceID = %q", err, res.TraceID)
	}
}

func TestRun_NeverSendsBaggage(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "ok", ""))
	if _, err := Run(context.Background(), cfg(s.URL)); err != nil {
		t.Fatal(err)
	}
	if v := c.headers[0].Get("baggage"); v != "" {
		t.Fatalf("baggage %q", v)
	}
}
