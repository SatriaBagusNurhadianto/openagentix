package obs

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestNilMetricsIsANoOp(t *testing.T) {
	var m *Metrics
	m.HTTP("GET /x", "GET", 200, time.Second)
	m.ToolCall("kb_search", "ok", time.Second)
	m.Search("hybrid", "ok", time.Second)
	m.EmbedCall("batch", "ok", time.Second)
	m.Ingest("ok")
	m.GraphJob("done")
	m.GraphModelCall("error", "http_429")
	m.GraphTokens(10)
	m.GraphPaused(time.Second)
	m.SetGraphSize("all", 1, 2, 3, 4)
	m.ResetGraphSize()
	m.OnScrape(func() {})
}

func TestEveryMetricAppearsWithItsLabels(t *testing.T) {
	m := New("v9.9.9")
	m.HTTP("PUT /v1/collections/{collection}/documents/{id}", "PUT", 201, 120*time.Millisecond)
	m.ToolCall("kb_search", "ok", 30*time.Millisecond)
	m.Search("hybrid", "ok", 30*time.Millisecond)
	m.EmbedCall("query", "timeout", 3*time.Second)
	m.Ingest("too_large")
	m.GraphJob("done")
	m.GraphModelCall("error", "http_429")
	m.GraphModelCall("ok", "")
	m.GraphTokens(1234)
	m.GraphPaused(90 * time.Second)
	m.SetGraphSize("all", 11, 22, 3, 4)
	out := scrape(t, m)
	for _, want := range []string{
		`openagentix_kb_http_requests_total{route="PUT /v1/collections/{collection}/documents/{id}",method="PUT",status_class="2xx"} 1`,
		`openagentix_kb_http_request_duration_seconds_count{route="PUT /v1/collections/{collection}/documents/{id}",method="PUT"} 1`,
		`openagentix_kb_mcp_tool_calls_total{tool="kb_search",outcome="ok"} 1`,
		`openagentix_kb_mcp_tool_call_duration_seconds_count{tool="kb_search"} 1`,
		`openagentix_kb_search_requests_total{mode="hybrid",outcome="ok"} 1`,
		`openagentix_kb_search_duration_seconds_count{mode="hybrid"} 1`,
		`openagentix_kb_embedder_calls_total{op="query",outcome="timeout"} 1`,
		`openagentix_kb_embedder_call_duration_seconds_count{op="query"} 1`,
		`openagentix_kb_documents_ingested_total{outcome="too_large"} 1`,
		`openagentix_kb_graph_jobs_total{state="done"} 1`,
		`openagentix_kb_graph_model_calls_total{outcome="error",code="http_429"} 1`,
		`openagentix_kb_graph_model_calls_total{outcome="ok",code="none"} 1`,
		`openagentix_kb_graph_extraction_tokens_total 1234`,
		`openagentix_kb_graph_budget_paused_seconds_total 90`,
		`openagentix_kb_graph_entities{collection="all"} 11`,
		`openagentix_kb_graph_relations{collection="all"} 22`,
		`openagentix_kb_graph_jobs_pending{collection="all"} 3`,
		`openagentix_kb_graph_factsets{collection="all"} 4`,
		`openagentix_kb_build_info{version="v9.9.9"} 1`,
		`go_goroutines `,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestUnknownLabelValuesAreFoldedNotExported(t *testing.T) {
	m := New("dev")
	m.ToolCall("my secret tool name", "weird", time.Millisecond)
	m.Search("surprise", "odd", time.Millisecond)
	m.GraphModelCall("error", "free text with a document title")
	m.HTTP("", "BREW", 799, time.Millisecond)
	m.GraphJob("who knows")
	out := scrape(t, m)
	for _, bad := range []string{"secret", "weird", "surprise", "document title", "BREW", "799", "who knows"} {
		if strings.Contains(out, bad) {
			t.Errorf("unbounded value %q reached the output", bad)
		}
	}
	for _, want := range []string{
		`tool="other",outcome="error"`, `mode="other",outcome="error"`, `code="other"`,
		`route="unmatched",method="OTHER",status_class="other"`, `graph_jobs_total{state="other"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing folded value %q in:\n%s", want, out)
		}
	}
}

func TestGraphSizeReplacesPreviousSample(t *testing.T) {
	m := New("dev")
	m.SetGraphSize("a", 1, 1, 1, 1)
	m.ResetGraphSize()
	m.SetGraphSize("b", 2, 2, 2, 2)
	out := scrape(t, m)
	if strings.Contains(out, `collection="a"`) || !strings.Contains(out, `graph_entities{collection="b"} 2`) {
		t.Fatalf("got:\n%s", out)
	}
}
