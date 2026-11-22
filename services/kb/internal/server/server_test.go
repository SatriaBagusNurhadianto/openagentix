package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openagentix/openagentix/services/kb/internal/chunk"
	"github.com/openagentix/openagentix/services/kb/internal/config"
	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/kb"
	"github.com/openagentix/openagentix/services/kb/internal/store"
	"github.com/openagentix/openagentix/services/kb/internal/store/memory"
)

func deps(st store.Store, cfg config.Config, logs io.Writer) Deps {
	log := slog.New(slog.NewJSONHandler(logs, nil))
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	svc := &kb.Service{Store: st, Embedder: embed.None{}, Chunk: chunk.Options{MaxChars: 200}, Log: log}
	return Deps{Svc: svc, Cfg: cfg, Log: log, Version: "test"}
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

type pingStore struct {
	*memory.Store
	err error
}

func (p pingStore) Ping(context.Context) error { return p.err }

func TestHealthzAndReadyz(t *testing.T) {
	h := Handler(deps(pingStore{memory.New(0), nil}, config.Config{}, io.Discard))
	if rec := get(h, "GET", "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz %d", rec.Code)
	}
	if rec := get(h, "GET", "/readyz"); rec.Code != 200 {
		t.Fatalf("readyz %d", rec.Code)
	}
	h = Handler(deps(pingStore{memory.New(0), errors.New("db password=hunter2 down")}, config.Config{}, io.Discard))
	rec := get(h, "GET", "/readyz")
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("readyz with broken store: %d %s", rec.Code, rec.Body)
	}
	if rec := get(h, "GET", "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz must stay up when the store is down: %d", rec.Code)
	}
}

func TestRoutesWired(t *testing.T) {
	h := Handler(deps(memory.New(0), config.Config{AdminToken: "t", MCPStateless: true}, io.Discard))
	if rec := get(h, "GET", "/v1/collections"); rec.Code != 401 {
		t.Fatalf("/v1 without token: %d", rec.Code)
	}
	if rec := get(h, "GET", "/nope"); rec.Code != 404 {
		t.Fatalf("unknown path: %d", rec.Code)
	}
}

func TestEndToEndIngestThenMCPSearch(t *testing.T) {
	h := Handler(deps(memory.New(0), config.Config{AdminToken: "t", MCPStateless: true}, io.Discard))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/v1/collections/ops/documents/oncall", strings.NewReader(`{"title":"On-call guide","source_uri":"https://wiki.example/oncall","text":"Page the secondary when the primary does not acknowledge within five minutes."}`))
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ingest: %v %v", err, resp)
	}
	resp.Body.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "kb_search", Arguments: map[string]any{"query": "secondary acknowledge"}})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "On-call guide") {
		t.Fatalf("text: %s", txt)
	}
}

type slowStore struct{ *memory.Store }

func (slowStore) ListCollections(ctx context.Context) ([]store.CollectionInfo, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRequestTimeoutYields504(t *testing.T) {
	h := Handler(deps(slowStore{memory.New(0)}, config.Config{AdminAllowAnonymous: true, RequestTimeout: 50 * time.Millisecond}, io.Discard))
	start := time.Now()
	rec := get(h, "GET", "/v1/collections")
	if rec.Code != http.StatusGatewayTimeout || time.Since(start) > 2*time.Second {
		t.Fatalf("code=%d elapsed=%v", rec.Code, time.Since(start))
	}
}

func TestAccessLogOmitsSecretsAndBodies(t *testing.T) {
	var logs bytes.Buffer
	h := Handler(deps(memory.New(0), config.Config{AdminToken: "tok-secret-123"}, &logs))
	req := httptest.NewRequest("PUT", "/v1/collections/c/documents/d", strings.NewReader(`{"text":"body-secret-456"}`))
	req.Header.Set("Authorization", "Bearer tok-secret-123")
	h.ServeHTTP(httptest.NewRecorder(), req)
	out := logs.String()
	if strings.Contains(out, "tok-secret-123") || strings.Contains(out, "body-secret-456") {
		t.Fatalf("log leaks: %s", out)
	}
	if !strings.Contains(out, `"status":200`) || !strings.Contains(out, "PUT") {
		t.Fatalf("access log missing: %s", out)
	}
}

type panicStore struct{ *memory.Store }

func (panicStore) ListCollections(context.Context) ([]store.CollectionInfo, error) { panic("boom") }

func TestPanicIsRecovered(t *testing.T) {
	h := Handler(deps(panicStore{memory.New(0)}, config.Config{AdminAllowAnonymous: true}, io.Discard))
	if rec := get(h, "GET", "/v1/collections"); rec.Code != 500 {
		t.Fatalf("code = %d", rec.Code)
	}
}

type blockingStore struct {
	*memory.Store
	entered chan struct{}
	release chan struct{}
}

func (b blockingStore) ListCollections(context.Context) ([]store.CollectionInfo, error) {
	close(b.entered)
	<-b.release
	return []store.CollectionInfo{{Name: "x", Documents: 1}}, nil
}

func TestGracefulShutdownDrainsInFlightRequests(t *testing.T) {
	b := blockingStore{memory.New(0), make(chan struct{}), make(chan struct{})}
	d := deps(b, config.Config{AdminAllowAnonymous: true, ShutdownGrace: 5 * time.Second}, io.Discard)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- Run(ctx, d, ln) }()

	var status atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.Get("http://" + ln.Addr().String() + "/v1/collections")
		if err == nil {
			status.Store(int32(resp.StatusCode))
			resp.Body.Close()
		}
	}()
	<-b.entered
	cancel() // SIGTERM equivalent while a request is in flight
	select {
	case <-runErr:
		t.Fatal("Run returned before the in-flight request finished")
	case <-time.After(200 * time.Millisecond):
	}
	close(b.release)
	<-done
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status.Load() != 200 {
		t.Fatalf("in-flight request status = %d", status.Load())
	}
	if _, err := http.Get("http://" + ln.Addr().String() + "/healthz"); err == nil {
		t.Fatal("server still accepting connections after shutdown")
	}
}

// hangEmbedder embeds instantly until hang is set, then blocks until the
// caller's context is cancelled or release is closed (test cleanup).
type hangEmbedder struct {
	hang    atomic.Bool
	release chan struct{}
}

func (h *hangEmbedder) Dimensions() int { return 3 }
func (h *hangEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if h.hang.Load() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-h.release:
			return nil, errors.New("released")
		}
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

// ctxStore fails Search on an expired context, like a real database driver.
type ctxStore struct{ *memory.Store }

func (c ctxStore) Search(ctx context.Context, q store.Query) ([]store.Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Store.Search(ctx, q)
}

func TestSlowEmbedderFallsBackToLexicalWithinDeadline(t *testing.T) {
	emb := &hangEmbedder{release: make(chan struct{})}
	d := deps(ctxStore{memory.New(3)}, config.Config{AdminToken: "t", MCPStateless: true, RequestTimeout: 3 * time.Second}, io.Discard)
	d.Svc.Embedder = emb
	d.Svc.QueryEmbedTimeout = 150 * time.Millisecond
	srv := httptest.NewServer(Handler(d))
	// Unblock any hung handler before Close, or Close waits for it forever.
	var once sync.Once
	unblock := func() { once.Do(func() { close(emb.release) }) }
	t.Cleanup(srv.Close)
	t.Cleanup(unblock)

	req, _ := http.NewRequest("PUT", srv.URL+"/v1/collections/ops/documents/oncall", strings.NewReader(`{"title":"On-call guide","text":"Page the secondary when the primary does not acknowledge."}`))
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ingest: %v %v", err, resp)
	}
	resp.Body.Close()
	emb.hang.Store(true)

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	callCtx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	start := time.Now()
	res, err := sess.CallTool(callCtx, &mcp.CallToolParams{Name: "kb_search", Arguments: map[string]any{"query": "secondary acknowledge"}})
	elapsed := time.Since(start)
	unblock() // never leave the handler hung: httptest teardown would wait for it
	if err != nil || res.IsError {
		t.Fatalf("search failed after %v: %v %+v", elapsed, err, res)
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "On-call guide") {
		t.Fatalf("no lexical fallback result: %s", txt)
	}
	if elapsed > time.Second {
		t.Fatalf("search took %v; the embedder consumed the request deadline", elapsed)
	}
}

type sleepEmbedder struct{ d time.Duration }

func (s sleepEmbedder) Dimensions() int { return 3 }
func (s sleepEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-time.After(s.d):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}

func TestIngestRouteHasItsOwnLongerTimeout(t *testing.T) {
	d := deps(memory.New(3), config.Config{AdminToken: "t", RequestTimeout: 150 * time.Millisecond, IngestTimeout: 3 * time.Second}, io.Discard)
	d.Svc.Embedder = sleepEmbedder{500 * time.Millisecond}
	h := Handler(d)
	put := httptest.NewRequest("PUT", "/v1/collections/c/documents/d", strings.NewReader(`{"text":"hello world"}`))
	put.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, put)
	if rec.Code != 200 {
		t.Fatalf("ingest: code=%d body=%s (the general request timeout applied)", rec.Code, rec.Body)
	}
}
