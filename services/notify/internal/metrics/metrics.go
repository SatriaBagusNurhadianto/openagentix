// Package metrics is a small Prometheus text-format exposition library: counters,
// gauges and fixed-bucket histograms with label sets, deterministic output and
// no dependencies. It exists so the service needs no client library for a
// handful of series.
//
// The same file set is kept, byte for byte, in services/kb/internal/metrics and
// services/notify/internal/metrics (the services are separate Go modules and
// are built from their own directory, so a shared module would need a replace
// directive and a wider Docker build context). A test in notify compares them.
//
// Cardinality is a design rule: label values must come from small closed sets.
// As a backstop every metric holds at most Registry.MaxSeries series; further
// label combinations are dropped (and counted), never exported.
package metrics

import (
	"bufio"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultMaxSeries bounds the series of one metric.
const DefaultMaxSeries = 500

// DefBuckets suit request and call latencies in seconds.
var DefBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30}

var (
	nameRe  = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

type collector interface {
	name() string
	write(w *bufio.Writer)
}

// Registry holds metrics and serves them.
type Registry struct {
	// MaxSeries is the per-metric series cap; set before registering metrics.
	// Zero means DefaultMaxSeries.
	MaxSeries int

	mu      sync.Mutex
	metrics map[string]collector
	hooks   []func()
	dropped atomic.Int64
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{metrics: map[string]collector{}}
}

// OnScrape registers f to run before every exposition, to refresh gauges that
// are sampled rather than updated in place. f must be quick.
func (r *Registry) OnScrape(f func()) {
	r.mu.Lock()
	r.hooks = append(r.hooks, f)
	r.mu.Unlock()
}

func (r *Registry) maxSeries() int {
	if r.MaxSeries > 0 {
		return r.MaxSeries
	}
	return DefaultMaxSeries
}

func (r *Registry) register(c collector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.metrics[c.name()]; dup {
		panic("metrics: duplicate metric " + c.name())
	}
	r.metrics[c.name()] = c
}

func checkNames(name string, labels []string) {
	if !nameRe.MatchString(name) {
		panic("metrics: invalid metric name " + strconv.Quote(name))
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) || strings.HasPrefix(l, "__") || l == "le" {
			panic("metrics: invalid label name " + strconv.Quote(l))
		}
	}
}

// vec is the label-set bookkeeping shared by all metric types.
type vec[T any] struct {
	reg    *Registry
	nm     string
	help   string
	typ    string
	labels []string
	newT   func() *T
	mu     sync.Mutex
	series map[string]*entry[T]
	max    int
	// discard absorbs updates for dropped or malformed label sets.
	discard *T
}

type entry[T any] struct {
	values []string
	v      *T
}

func newVec[T any](r *Registry, name, help, typ string, labels []string, newT func() *T) *vec[T] {
	checkNames(name, labels)
	return &vec[T]{reg: r, nm: name, help: help, typ: typ, labels: labels, newT: newT,
		series: map[string]*entry[T]{}, max: r.maxSeries(), discard: newT()}
}

func (v *vec[T]) name() string { return v.nm }

func (v *vec[T]) with(values []string) *T {
	if len(values) != len(v.labels) {
		return v.discard
	}
	key := strings.Join(values, "\xff")
	v.mu.Lock()
	defer v.mu.Unlock()
	if e, ok := v.series[key]; ok {
		return e.v
	}
	if len(v.series) >= v.max {
		v.reg.dropped.Add(1)
		return v.discard
	}
	e := &entry[T]{values: append([]string(nil), values...), v: v.newT()}
	v.series[key] = e
	return e.v
}

// sorted returns the series ordered by label values.
func (v *vec[T]) sorted() []*entry[T] {
	v.mu.Lock()
	out := make([]*entry[T], 0, len(v.series))
	for _, e := range v.series {
		out = append(out, e)
	}
	v.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].values, out[j].values
		for k := range a {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
	})
	return out
}

func (v *vec[T]) header(w *bufio.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", v.nm, escapeHelp(v.help), v.nm, v.typ)
}

// ---- counter and gauge ----

type value struct {
	mu sync.Mutex
	f  float64
}

func (x *value) add(d float64) { x.mu.Lock(); x.f += d; x.mu.Unlock() }
func (x *value) set(d float64) { x.mu.Lock(); x.f = d; x.mu.Unlock() }
func (x *value) get() float64  { x.mu.Lock(); defer x.mu.Unlock(); return x.f }

// Counter is a monotonically increasing value.
type Counter struct{ x *value }

// Inc adds one.
func (c Counter) Inc() { c.x.add(1) }

// Add adds d; negative values are ignored.
func (c Counter) Add(d float64) {
	if d > 0 {
		c.x.add(d)
	}
}

// CounterVec is a counter family.
type CounterVec struct{ v *vec[value] }

// Counter registers a counter family. Panics on an invalid or duplicate name.
func (r *Registry) Counter(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{newVec(r, name, help, "counter", labels, func() *value { return &value{} })}
	r.register(valueCollector{c.v})
	return c
}

// With returns the counter for the label values (one per label, in order).
func (c *CounterVec) With(values ...string) Counter { return Counter{c.v.with(values)} }

// Gauge is a value that goes up and down.
type Gauge struct{ x *value }

// Set sets the value.
func (g Gauge) Set(d float64) { g.x.set(d) }

// Add adds d, which may be negative.
func (g Gauge) Add(d float64) { g.x.add(d) }

// GaugeVec is a gauge family.
type GaugeVec struct{ v *vec[value] }

// Gauge registers a gauge family.
func (r *Registry) Gauge(name, help string, labels ...string) *GaugeVec {
	g := &GaugeVec{newVec(r, name, help, "gauge", labels, func() *value { return &value{} })}
	r.register(valueCollector{g.v})
	return g
}

// Reset removes every series, for gauges that are rebuilt at each scrape and
// must not keep label sets that have disappeared.
func (g *GaugeVec) Reset() {
	g.v.mu.Lock()
	g.v.series = map[string]*entry[value]{}
	g.v.mu.Unlock()
}

// With returns the gauge for the label values.
func (g *GaugeVec) With(values ...string) Gauge { return Gauge{g.v.with(values)} }

type valueCollector struct{ v *vec[value] }

func (c valueCollector) name() string { return c.v.nm }
func (c valueCollector) write(w *bufio.Writer) {
	es := c.v.sorted()
	if len(es) == 0 {
		return
	}
	c.v.header(w)
	for _, e := range es {
		writeSample(w, c.v.nm, c.v.labels, e.values, "", "", e.v.get())
	}
}

// ---- histogram ----

type hist struct {
	mu     sync.Mutex
	counts []uint64 // per bucket (not cumulative), last slot is +Inf
	sum    float64
}

// Histogram observes values into fixed buckets.
type Histogram struct {
	h       *hist
	buckets []float64
}

// Observe records one value.
func (h Histogram) Observe(x float64) {
	i := sort.SearchFloat64s(h.buckets, x) // first bucket with upper bound >= x
	h.h.mu.Lock()
	h.h.counts[i]++
	h.h.sum += x
	h.h.mu.Unlock()
}

// ObserveSince records the seconds elapsed since start.
func (h Histogram) ObserveSince(start time.Time) { h.Observe(time.Since(start).Seconds()) }

// HistogramVec is a histogram family.
type HistogramVec struct {
	v       *vec[hist]
	buckets []float64
}

// Histogram registers a histogram family. Buckets must be strictly increasing.
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *HistogramVec {
	for i, b := range buckets {
		if math.IsNaN(b) || math.IsInf(b, 0) || (i > 0 && b <= buckets[i-1]) {
			panic("metrics: histogram buckets must be finite and strictly increasing")
		}
	}
	bs := append([]float64(nil), buckets...)
	h := &HistogramVec{buckets: bs}
	h.v = newVec(r, name, help, "histogram", labels, func() *hist { return &hist{counts: make([]uint64, len(bs)+1)} })
	r.register(histCollector{h})
	return h
}

// With returns the histogram for the label values.
func (h *HistogramVec) With(values ...string) Histogram {
	return Histogram{h: h.v.with(values), buckets: h.buckets}
}

type histCollector struct{ h *HistogramVec }

func (c histCollector) name() string { return c.h.v.nm }
func (c histCollector) write(w *bufio.Writer) {
	v := c.h.v
	es := v.sorted()
	if len(es) == 0 {
		return
	}
	v.header(w)
	for _, e := range es {
		e.v.mu.Lock()
		counts := append([]uint64(nil), e.v.counts...)
		sum := e.v.sum
		e.v.mu.Unlock()
		var cum uint64
		for i, b := range c.h.buckets {
			cum += counts[i]
			writeSample(w, v.nm+"_bucket", v.labels, e.values, "le", formatFloat(b), float64(cum))
		}
		cum += counts[len(counts)-1]
		writeSample(w, v.nm+"_bucket", v.labels, e.values, "le", "+Inf", float64(cum))
		writeSample(w, v.nm+"_sum", v.labels, e.values, "", "", sum)
		writeSample(w, v.nm+"_count", v.labels, e.values, "", "", float64(cum))
	}
}

// ---- exposition ----

func writeSample(w *bufio.Writer, name string, labels, values []string, extraK, extraV string, val float64) {
	w.WriteString(name)
	if len(labels) > 0 || extraK != "" {
		w.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				w.WriteByte(',')
			}
			fmt.Fprintf(w, `%s="%s"`, l, escapeLabel(values[i]))
		}
		if extraK != "" {
			if len(labels) > 0 {
				w.WriteByte(',')
			}
			fmt.Fprintf(w, `%s="%s"`, extraK, escapeLabel(extraV))
		}
		w.WriteByte('}')
	}
	w.WriteByte(' ')
	w.WriteString(formatFloat(val))
	w.WriteByte('\n')
}

func formatFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	case math.IsNaN(f):
		return "NaN"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

var (
	labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }
func escapeHelp(s string) string  { return helpEscaper.Replace(s) }

// Write renders every metric, sorted by name.
func (r *Registry) Write(w *bufio.Writer) {
	r.mu.Lock()
	hooks := append([]func(){}, r.hooks...)
	r.mu.Unlock()
	for _, h := range hooks {
		h()
	}
	r.mu.Lock()
	names := make([]string, 0, len(r.metrics))
	for n := range r.metrics {
		names = append(names, n)
	}
	cs := make(map[string]collector, len(names))
	for n, c := range r.metrics {
		cs[n] = c
	}
	r.mu.Unlock()
	sort.Strings(names)
	for _, n := range names {
		cs[n].write(w)
	}
	if d := r.dropped.Load(); d > 0 {
		fmt.Fprintf(w, "# HELP openagentix_metrics_series_dropped_total Label sets refused because a metric reached its series cap.\n# TYPE openagentix_metrics_series_dropped_total counter\nopenagentix_metrics_series_dropped_total %d\n", d)
	}
}

// Handler serves GET and HEAD on exactly /metrics and nothing else.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/metrics" {
			http.NotFound(w, req)
			return
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if req.Method == http.MethodHead {
			return
		}
		bw := bufio.NewWriter(w)
		r.Write(bw)
		_ = bw.Flush()
	})
}

// RegisterRuntime adds the cheap process basics: goroutines, heap in use, start
// time, and <prefix>_build_info{version} = 1.
func RegisterRuntime(r *Registry, prefix, version string) {
	r.Gauge(prefix+"_build_info", "Build information; the value is always 1.", "version").With(version).Set(1)
	r.Gauge("process_start_time_seconds", "Start time of the process as a Unix timestamp.").With().Set(float64(time.Now().Unix()))
	gr := r.Gauge("go_goroutines", "Number of goroutines.").With()
	mem := r.Gauge("go_memstats_alloc_bytes", "Bytes of allocated heap objects in use.").With()
	r.OnScrape(func() {
		gr.Set(float64(runtime.NumGoroutine()))
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		mem.Set(float64(ms.Alloc))
	})
}
