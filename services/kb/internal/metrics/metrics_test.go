package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func render(t *testing.T, r *Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	return rec.Body.String()
}

func TestCounterTextFormat(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("app_requests_total", "Requests served.", "route", "code")
	c.With("/b", "2xx").Inc()
	c.With("/a", "5xx").Add(2)
	c.With("/b", "2xx").Inc()
	got := render(t, r)
	want := "# HELP app_requests_total Requests served.\n" +
		"# TYPE app_requests_total counter\n" +
		`app_requests_total{route="/a",code="5xx"} 2` + "\n" +
		`app_requests_total{route="/b",code="2xx"} 2` + "\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCounterIgnoresNegativeAdd(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("x_total", "x").With()
	c.Add(-5)
	c.Add(1)
	if got := render(t, r); !strings.Contains(got, "x_total 1\n") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestGauge(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("app_pending", "Pending.", "state")
	g.With("a").Set(3)
	g.With("a").Add(-1.5)
	got := render(t, r)
	if !strings.Contains(got, "# TYPE app_pending gauge\n") || !strings.Contains(got, `app_pending{state="a"} 1.5`+"\n") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestHistogramCumulative(t *testing.T) {
	r := NewRegistry()
	h := r.Histogram("app_seconds", "Latency.", []float64{0.1, 1}, "route")
	h.With("/x").Observe(0.05)
	h.With("/x").Observe(0.5)
	h.With("/x").Observe(7)
	got := render(t, r)
	want := "# HELP app_seconds Latency.\n# TYPE app_seconds histogram\n" +
		`app_seconds_bucket{route="/x",le="0.1"} 1` + "\n" +
		`app_seconds_bucket{route="/x",le="1"} 2` + "\n" +
		`app_seconds_bucket{route="/x",le="+Inf"} 3` + "\n" +
		`app_seconds_sum{route="/x"} 7.55` + "\n" +
		`app_seconds_count{route="/x"} 3` + "\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestHistogramWithoutLabels(t *testing.T) {
	r := NewRegistry()
	r.Histogram("h_seconds", "h", []float64{1}).With().Observe(2)
	got := render(t, r)
	if !strings.Contains(got, `h_seconds_bucket{le="1"} 0`+"\n") || !strings.Contains(got, "h_seconds_count 1\n") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestLabelAndHelpEscaping(t *testing.T) {
	r := NewRegistry()
	r.Counter("e_total", "line one\nback\\slash", "l").With("v").Inc()
	r.Counter("e_total2", "x", "l").With("é ü").Inc()
	got := render(t, r)
	if !strings.Contains(got, `# HELP e_total line one\nback\\slash`+"\n") {
		t.Fatalf("help not escaped:\n%s", got)
	}
	r2 := NewRegistry()
	r2.Counter("e_total", "h", "l").With("quote\" back\\ nl\n").Inc()
	if g := render(t, r2); !strings.Contains(g, `e_total{l="quote\" back\\ nl\n"} 1`+"\n") {
		t.Fatalf("label not escaped:\n%s", g)
	}
	if !strings.Contains(got, `e_total2{l="é ü"} 1`) {
		t.Fatalf("utf-8 must pass through:\n%s", got)
	}
}

func TestOutputDeterministicAndSorted(t *testing.T) {
	r := NewRegistry()
	r.Counter("zz_total", "z").With().Inc()
	r.Counter("aa_total", "a").With().Inc()
	a, b := render(t, r), render(t, r)
	if a != b {
		t.Fatal("output differs between scrapes")
	}
	if strings.Index(a, "aa_total") > strings.Index(a, "zz_total") {
		t.Fatalf("metrics not sorted by name:\n%s", a)
	}
}

func TestInvalidNamesPanic(t *testing.T) {
	for _, f := range []func(){
		func() { NewRegistry().Counter("bad-name", "h") },
		func() { NewRegistry().Counter("ok_total", "h", "bad-label") },
		func() { NewRegistry().Counter("ok_total", "h", "le_ok", "__reserved") },
		func() { NewRegistry().Histogram("h", "h", []float64{2, 1}) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected panic")
				}
			}()
			f()
		}()
	}
	r := NewRegistry()
	r.Counter("dup_total", "h")
	defer func() {
		if recover() == nil {
			t.Error("duplicate registration must panic")
		}
	}()
	r.Counter("dup_total", "h")
}

func TestWrongLabelCountDoesNotPanicOrExport(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("w_total", "w", "a")
	c.With("1", "2").Inc()
	c.With().Inc()
	if got := render(t, r); strings.Contains(got, "w_total{") {
		t.Fatalf("mismatched series exported:\n%s", got)
	}
}

func TestSeriesCapBoundsCardinality(t *testing.T) {
	r := NewRegistry()
	r.MaxSeries = 3
	c := r.Counter("cap_total", "c", "l")
	for _, v := range []string{"a", "b", "c", "d", "e"} {
		c.With(v).Inc()
	}
	got := render(t, r)
	if n := strings.Count(got, "cap_total{"); n != 3 {
		t.Fatalf("want 3 series, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "openagentix_metrics_series_dropped_total 2\n") {
		t.Fatalf("dropped counter missing:\n%s", got)
	}
	c.With("a").Inc() // an existing series keeps working
	if !strings.Contains(render(t, r), `cap_total{l="a"} 2`) {
		t.Fatal("existing series stopped counting")
	}
}

func TestOnScrapeRunsBeforeRender(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("s_gauge", "s").With()
	n := 0.0
	r.OnScrape(func() { n++; g.Set(n) })
	render(t, r)
	if got := render(t, r); !strings.Contains(got, "s_gauge 2\n") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestHandlerServesOnlyMetrics(t *testing.T) {
	r := NewRegistry()
	h := r.Handler()
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/metrics", 200}, {"HEAD", "/metrics", 200}, {"POST", "/metrics", 405},
		{"GET", "/", 404}, {"GET", "/v1/collections", 404}, {"GET", "/mcp", 404}, {"GET", "/healthz", 404}, {"GET", "/metrics/x", 404},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
}

func TestConcurrent(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("c_total", "c", "l")
	h := r.Histogram("c_seconds", "c", []float64{1}, "l")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				c.With("a").Inc()
				h.With("a").Observe(0.5)
				if j%100 == 0 {
					rec := httptest.NewRecorder()
					r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
					_, _ = io.Copy(io.Discard, rec.Body)
				}
			}
		}()
	}
	wg.Wait()
	if got := render(t, r); !strings.Contains(got, `c_total{l="a"} 8000`+"\n") || !strings.Contains(got, `c_seconds_count{l="a"} 8000`+"\n") {
		t.Fatalf("lost updates:\n%s", got)
	}
}

func TestRuntimeMetrics(t *testing.T) {
	r := NewRegistry()
	RegisterRuntime(r, "openagentix_kb", "v1.2.3")
	got := render(t, r)
	for _, want := range []string{
		`openagentix_kb_build_info{version="v1.2.3"} 1`,
		"# TYPE go_goroutines gauge",
		"# TYPE go_memstats_alloc_bytes gauge",
		"# TYPE process_start_time_seconds gauge",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestGaugeReset(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("rs_gauge", "r", "l")
	g.With("a").Set(1)
	g.Reset()
	if got := render(t, r); strings.Contains(got, "rs_gauge{") {
		t.Fatalf("series survived Reset:\n%s", got)
	}
	g.With("b").Set(2)
	if got := render(t, r); !strings.Contains(got, `rs_gauge{l="b"} 2`) {
		t.Fatalf("gauge unusable after Reset:\n%s", got)
	}
}
