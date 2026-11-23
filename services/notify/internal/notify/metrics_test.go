package notify

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/obs"
)

func scrapeObs(m *obs.Metrics) string {
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestSendMetricsByKindAndOutcome(t *testing.T) {
	m := obs.New("test")
	r := newRig(t, func(o *Options) { o.Metrics = m; o.GlobalRatePerMinute = 100 })
	ctx := context.Background()

	_, _ = r.svc.Send(ctx, Request{Destination: "ops", Text: "canary-message-text one"})                       // delivered
	_, _ = r.svc.Send(ctx, Request{Destination: "ops", Text: "canary-message-text two", IdempotencyKey: "k1"}) // delivered
	_, _ = r.svc.Send(ctx, Request{Destination: "ops", Text: "canary-message-text two", IdempotencyKey: "k1"}) // duplicate
	_, _ = r.svc.Send(ctx, Request{Destination: "canary-destination-name", Text: "x"})                         // unknown
	_, _ = r.svc.Send(ctx, Request{Destination: "team", Text: ""})                                             // rejected_validation
	_, _ = r.svc.Send(ctx, Request{Destination: "team", Text: strings.Repeat("canary-long ", 50)})             // payload
	_, _ = r.svc.Send(ctx, Request{Destination: "team", Text: "ok", Title: strings.Repeat("t", 900)})          // payload (title)
	r.sl.err = &delivery.Error{Kind: delivery.Upstream, Detail: "canary-upstream-detail", Definite: true}
	_, _ = r.svc.Send(ctx, Request{Destination: "team", Text: "canary-message-text three"}) // upstream_error
	r.sl.err = nil
	// the per-destination limit is 3 per minute: "ops" already used 2
	_, _ = r.svc.Send(ctx, Request{Destination: "ops", Text: "a"})
	_, err := r.svc.Send(ctx, Request{Destination: "ops", Text: "b"})
	if kindOf(err) != RateLimited {
		t.Fatalf("want rate limited, got %v", err)
	}

	out := scrapeObs(m)
	for _, want := range []string{
		`openagentix_notify_sends_total{kind="telegram",outcome="delivered"} 3`,
		`openagentix_notify_sends_total{kind="telegram",outcome="duplicate"} 1`,
		`openagentix_notify_sends_total{kind="unknown",outcome="unknown_destination"} 1`,
		`openagentix_notify_sends_total{kind="slack",outcome="rejected_validation"} 3`,
		`openagentix_notify_sends_total{kind="slack",outcome="upstream_error"} 1`,
		`openagentix_notify_sends_total{kind="telegram",outcome="rate_limited"} 1`,
		`openagentix_notify_send_duration_seconds_count{kind="telegram"} 5`,
		`openagentix_notify_rate_limit_rejections_total{scope="destination"} 1`,
		`openagentix_notify_payload_rejections_total{kind="slack",field="text"} 1`,
		`openagentix_notify_payload_rejections_total{kind="slack",field="title"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, canary := range []string{"canary", "ops", "team", "k1"} {
		if strings.Contains(out, canary) {
			t.Errorf("forbidden %q in metrics:\n%s", canary, out)
		}
	}
}

func TestGlobalRateLimitMetric(t *testing.T) {
	m := obs.New("test")
	r := newRig(t, func(o *Options) { o.Metrics = m; o.GlobalRatePerMinute = 1 })
	_, _ = r.svc.Send(context.Background(), Request{Destination: "ops", Text: "a"})
	_, err := r.svc.Send(context.Background(), Request{Destination: "team", Text: "b"})
	if kindOf(err) != RateLimited {
		t.Fatalf("err = %v", err)
	}
	out := scrapeObs(m)
	if !strings.Contains(out, `openagentix_notify_rate_limit_rejections_total{scope="global"} 1`) ||
		!strings.Contains(out, `openagentix_notify_sends_total{kind="slack",outcome="rate_limited"} 1`) {
		t.Fatalf("got:\n%s", out)
	}
}

func TestUnexpectedSenderErrorIsUpstreamError(t *testing.T) {
	m := obs.New("test")
	r := newRig(t, func(o *Options) { o.Metrics = m })
	r.tg.err = errors.New("secret-token-123 leaked in a raw error")
	_, _ = r.svc.Send(context.Background(), Request{Destination: "ops", Text: "a"})
	out := scrapeObs(m)
	if !strings.Contains(out, `openagentix_notify_sends_total{kind="telegram",outcome="upstream_error"} 1`) || strings.Contains(out, "secret") {
		t.Fatalf("got:\n%s", out)
	}
}
