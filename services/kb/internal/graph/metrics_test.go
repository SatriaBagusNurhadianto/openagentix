package graph

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/kb/internal/obs"
)

func scrapeObs(m *obs.Metrics) string {
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestWorkerMetricsRetryThenDone(t *testing.T) {
	m := obs.New("test")
	r := newRig(t, func(n int, req map[string]any) string {
		if n == 1 {
			return `{"entities": [ {"name": "Checkout`
		}
		return goodAnswer(n, req)
	}, func(o *Options) { o.Metrics = m })
	r.gw.tokens = 100
	r.put("d", chunkA)
	r.settle()
	out := scrapeObs(m)
	for _, want := range []string{
		`openagentix_kb_graph_jobs_total{state="retry"} 1`,
		`openagentix_kb_graph_jobs_total{state="done"} 1`,
		`openagentix_kb_graph_model_calls_total{outcome="error",code="malformed_output"} 1`,
		`openagentix_kb_graph_model_calls_total{outcome="ok",code="none"} 1`,
		`openagentix_kb_graph_extraction_tokens_total 200`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Checkout") || strings.Contains(out, "docs") {
		t.Errorf("document content or collection leaked:\n%s", out)
	}
}

func TestWorkerMetricsFinalFailureAndStaleWindow(t *testing.T) {
	m := obs.New("test")
	r := newRig(t, goodAnswer, func(o *Options) { o.Metrics = m })
	r.gw.status = func(int) int { return http.StatusBadRequest }
	r.put("d", chunkA)
	r.drain()
	out := scrapeObs(m)
	for _, want := range []string{
		`openagentix_kb_graph_jobs_total{state="failed"} 1`,
		`openagentix_kb_graph_model_calls_total{outcome="error",code="http_4xx"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestWorkerMetricsBudgetPausedSeconds(t *testing.T) {
	m := obs.New("test")
	r := newRig(t, goodAnswer, func(o *Options) { o.Metrics = m; o.TokensPerHour = 100 })
	r.gw.tokens = 600
	r.put("a", chunkA)
	r.put("b", chunkA+" b")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_ = r.w.Run(ctx) // runs job one, then waits for the budget until the context ends
	out := scrapeObs(m)
	var secs float64
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, "openagentix_kb_graph_budget_paused_seconds_total "); ok {
			secs = parseF(t, v)
		}
	}
	if secs <= 0 || secs > 5 {
		t.Fatalf("paused seconds = %v, want a short positive wait:\n%s", secs, out)
	}
}

func parseF(t *testing.T, s string) float64 {
	t.Helper()
	var f float64
	if _, err := fmt.Sscan(s, &f); err != nil {
		t.Fatal(err)
	}
	return f
}
