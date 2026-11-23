package kb

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/obs"
)

func scrape(m *obs.Metrics) string {
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestSearchMetricsModeAndOutcome(t *testing.T) {
	ctx := context.Background()
	m := obs.New("test")

	lex := newSvc(embed.None{})
	lex.Metrics = m
	_, _ = lex.Put(ctx, "docs", "d1", DocInput{Text: "bananas are yellow"})
	if _, err := lex.Search(ctx, "bananas", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := lex.Search(ctx, "  ", "", 0); err == nil {
		t.Fatal("empty query must fail")
	}

	hyb := newSvc(&fakeEmbedder{dim: 4})
	hyb.Metrics = m
	_, _ = hyb.Search(ctx, "apple", "", 0)

	broken := newSvc(&fakeEmbedder{dim: 4, err: errors.New("down")})
	broken.Metrics = m
	_, _ = broken.Search(ctx, "apple", "", 0) // falls back to lexical, still ok

	out := scrape(m)
	for _, want := range []string{
		`openagentix_kb_search_requests_total{mode="lexical",outcome="ok"} 2`,
		`openagentix_kb_search_requests_total{mode="lexical",outcome="invalid"} 1`,
		`openagentix_kb_search_requests_total{mode="hybrid",outcome="ok"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, canary := range []string{"bananas", "apple"} {
		if strings.Contains(out, canary) {
			t.Errorf("query text %q leaked into metrics", canary)
		}
	}
}

func TestPutMetricsOutcomes(t *testing.T) {
	ctx := context.Background()
	m := obs.New("test")
	s := newSvc(embed.None{})
	s.Metrics = m
	s.MaxChunksPerDoc = 2
	_, _ = s.Put(ctx, "docs", "ok1", DocInput{Text: "short"})
	_, _ = s.Put(ctx, "bad collection!", "x", DocInput{Text: "short"})
	_, _ = s.Put(ctx, "docs", "big", DocInput{Text: strings.Repeat("word ", 100)})
	up := newSvc(&fakeEmbedder{dim: 4, err: errors.New("embedder down")})
	up.Metrics = m
	_, _ = up.Put(ctx, "docs", "u", DocInput{Text: "short"})
	out := scrape(m)
	for _, want := range []string{
		`openagentix_kb_documents_ingested_total{outcome="ok"} 1`,
		`openagentix_kb_documents_ingested_total{outcome="invalid"} 1`,
		`openagentix_kb_documents_ingested_total{outcome="too_large"} 1`,
		`openagentix_kb_documents_ingested_total{outcome="upstream_error"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "bad collection") || strings.Contains(out, "ok1") {
		t.Errorf("collection or id leaked:\n%s", out)
	}
}

func TestGraphGaugesSampledAtScrapeWithCache(t *testing.T) {
	ctx := context.Background()
	s := graphSvc()
	m := obs.New("test")
	now := time.Unix(1000, 0)
	s.RegisterGraphGauges(m, GraphGaugeOptions{TTL: 15 * time.Second, Now: func() time.Time { return now }})
	if _, err := s.Put(ctx, "kg", "d1", DocInput{Text: "Basalt escalates to Priya Venkat."}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutFactSet(ctx, "kg", "org", catalogue()); err != nil {
		t.Fatal(err)
	}
	out := scrape(m)
	for _, want := range []string{
		`openagentix_kb_graph_entities{collection="all"} 3`,
		`openagentix_kb_graph_relations{collection="all"} 1`,
		`openagentix_kb_graph_factsets{collection="all"} 1`,
		`openagentix_kb_graph_jobs_pending{collection="all"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := s.DeleteFactSet(ctx, "kg", "org"); err != nil {
		t.Fatal(err)
	}
	if out := scrape(m); !strings.Contains(out, `graph_factsets{collection="all"} 1`) {
		t.Errorf("sample must be cached within the TTL:\n%s", out)
	}
	now = now.Add(16 * time.Second)
	if out := scrape(m); !strings.Contains(out, `graph_factsets{collection="all"} 0`) {
		t.Errorf("sample must refresh after the TTL:\n%s", out)
	}
	if strings.Contains(scrape(m), `collection="kg"`) {
		t.Error("collection name used as a label without the flag")
	}
}

func TestGraphGaugesPerCollectionBehindFlag(t *testing.T) {
	ctx := context.Background()
	s := graphSvc()
	m := obs.New("test")
	s.RegisterGraphGauges(m, GraphGaugeOptions{TTL: time.Second, PerCollection: true})
	_, _ = s.Put(ctx, "kg", "d1", DocInput{Text: "Basalt escalates to Priya Venkat."})
	_, _ = s.PutFactSet(ctx, "kg", "org", catalogue())
	if out := scrape(m); !strings.Contains(out, `openagentix_kb_graph_entities{collection="kg"} 3`) {
		t.Errorf("per-collection gauge missing:\n%s", out)
	}
}

func TestGraphGaugesDisabledGraphRegistersNothing(t *testing.T) {
	s := newSvc(embed.None{})
	m := obs.New("test")
	s.RegisterGraphGauges(m, GraphGaugeOptions{})
	if out := scrape(m); strings.Contains(out, "graph_entities") {
		t.Errorf("graph gauges exported with the graph off:\n%s", out)
	}
}
