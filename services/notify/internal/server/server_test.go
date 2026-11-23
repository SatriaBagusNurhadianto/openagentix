package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/openagentix/openagentix/services/notify/internal/config"
	"github.com/openagentix/openagentix/services/notify/internal/delivery"
	"github.com/openagentix/openagentix/services/notify/internal/destinations"
	"github.com/openagentix/openagentix/services/notify/internal/notify"
)

type sender struct {
	entered chan struct{}
	release chan struct{} // nil: return at once
	calls   atomic.Int32
}

func (s *sender) Send(ctx context.Context, _ delivery.Message) error {
	s.calls.Add(1)
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return &delivery.Error{Kind: delivery.Timeout, Detail: "the destination did not answer in time"}
		}
	}
	return nil
}

func deps(s *sender, cfg config.Config, logs io.Writer, withDest bool) Deps {
	log := slog.New(slog.NewJSONHandler(logs, nil))
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	cfg.MCPStateless = true
	var ds []destinations.Destination
	if withDest {
		ds = []destinations.Destination{{Name: "ops", Description: "Ops", Platform: "telegram", Limits: config.Limits{MaxTextChars: 100, RatePerMinute: 100, RatePerHour: 100}, Sender: s}}
	}
	svc := notify.New(notify.Options{Destinations: ds, Label: "Test", MaxInFlight: 4, IdempotencyWindow: time.Minute, IdempotencyMaxEntries: 10, Log: log})
	return Deps{Svc: svc, Cfg: cfg, Log: log, Version: "test"}
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthzIsAlwaysOK(t *testing.T) {
	for _, withDest := range []bool{true, false} {
		h := Handler(deps(&sender{}, config.Config{}, io.Discard, withDest))
		if rec := get(h, "GET", "/healthz"); rec.Code != 200 {
			t.Fatalf("withDest=%v healthz %d", withDest, rec.Code)
		}
	}
}

func TestReadyzReflectsWhetherDestinationsAreConfigured(t *testing.T) {
	h := Handler(deps(&sender{}, config.Config{}, io.Discard, true))
	if rec := get(h, "GET", "/readyz"); rec.Code != 200 {
		t.Fatalf("readyz %d", rec.Code)
	}
	h = Handler(deps(&sender{}, config.Config{}, io.Discard, false))
	rec := get(h, "GET", "/readyz")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "no destinations") {
		t.Fatalf("readyz without destinations: %d %q", rec.Code, rec.Body)
	}
}

func TestUnknownPathsAreNotFoundAndProbesRejectPOST(t *testing.T) {
	h := Handler(deps(&sender{}, config.Config{}, io.Discard, true))
	if rec := get(h, "GET", "/v1/anything"); rec.Code != 404 {
		t.Fatalf("code = %d", rec.Code)
	}
	if rec := get(h, "POST", "/healthz"); rec.Code != 405 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func connect(t *testing.T, url string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: url + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func TestMCPEndToEndThroughTheFullHandler(t *testing.T) {
	s := &sender{}
	srv := httptest.NewServer(Handler(deps(s, config.Config{}, io.Discard, true)))
	t.Cleanup(srv.Close)
	sess := connect(t, srv.URL)
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "notify_send", Arguments: map[string]any{"destination": "ops", "text": "hi"}})
	if err != nil || res.IsError || s.calls.Load() != 1 {
		t.Fatalf("res=%+v err=%v calls=%d", res, err, s.calls.Load())
	}
}

func TestMCPToolCallHonoursRequestDeadline(t *testing.T) {
	s := &sender{release: make(chan struct{})}
	t.Cleanup(func() { close(s.release) })
	srv := httptest.NewServer(Handler(deps(s, config.Config{RequestTimeout: 300 * time.Millisecond}, io.Discard, true)))
	t.Cleanup(srv.Close)
	sess := connect(t, srv.URL)
	start := time.Now()
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "notify_send", Arguments: map[string]any{"destination": "ops", "text": "hi"}})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("tool call took %v with a 300ms request deadline", elapsed)
	}
	if err == nil && !res.IsError {
		t.Fatalf("expected an error result, got %+v", res)
	}
}

func TestAccessLogOmitsQueryHeadersAndBodies(t *testing.T) {
	var logs bytes.Buffer
	h := Handler(deps(&sender{}, config.Config{}, &logs, true))
	req := httptest.NewRequest("POST", "/mcp?token=query-secret", strings.NewReader(`{"text":"body-secret"}`))
	req.Header.Set("Authorization", "Bearer header-secret")
	h.ServeHTTP(httptest.NewRecorder(), req)
	out := logs.String()
	for _, leak := range []string{"query-secret", "header-secret", "body-secret"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaks %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, `"path":"/mcp"`) || !strings.Contains(out, `"status"`) {
		t.Fatalf("access log missing: %s", out)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := recoverer(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	if rec := get(h, "GET", "/x"); rec.Code != 500 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestGracefulShutdownDrainsInFlightRequests(t *testing.T) {
	s := &sender{entered: make(chan struct{}, 1), release: make(chan struct{})}
	d := deps(s, config.Config{ShutdownGrace: 5 * time.Second}, io.Discard, true)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- Run(ctx, d, ln) }()

	sess := connect(t, "http://"+ln.Addr().String())
	var isErr atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "notify_send", Arguments: map[string]any{"destination": "ops", "text": "hi"}})
		isErr.Store(err != nil || res.IsError)
	}()
	<-s.entered
	cancel() // SIGTERM equivalent while a send is in flight
	select {
	case <-runErr:
		t.Fatal("Run returned before the in-flight request finished")
	case <-time.After(200 * time.Millisecond):
	}
	close(s.release)
	<-done
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if isErr.Load() {
		t.Fatal("in-flight send was cut off by shutdown")
	}
	if _, err := http.Get("http://" + ln.Addr().String() + "/healthz"); err == nil {
		t.Fatal("server still accepting connections after shutdown")
	}
}

func postMCP(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func callBody(text string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"notify_send","arguments":{"destination":"ops","text":"` + text + `"}}}`
}

func TestMCPBodyOverTheLimitIsRejectedBeforeTheToolRuns(t *testing.T) {
	s := &sender{}
	for _, cfg := range []config.Config{{}, {MaxRequestBytes: 2048}} {
		h := Handler(deps(s, cfg, io.Discard, true))
		rec := postMCP(h, callBody(strings.Repeat("a", 200<<10)))
		if rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("cfg %+v: status %d body %.200s", cfg, rec.Code, rec.Body)
		}
	}
	if s.calls.Load() != 0 {
		t.Fatalf("tool ran %d times", s.calls.Load())
	}
}

func TestMCPBodyUnderTheLimitStillWorks(t *testing.T) {
	s := &sender{}
	h := Handler(deps(s, config.Config{}, io.Discard, true))
	rec := postMCP(h, callBody("hello"))
	if rec.Code != 200 || s.calls.Load() != 1 {
		t.Fatalf("status %d calls %d body %.200s", rec.Code, s.calls.Load(), rec.Body)
	}
}

func TestRecoverer_RePanicsErrAbortHandler(t *testing.T) {
	var logs bytes.Buffer
	h := recoverer(slog.New(slog.NewJSONHandler(&logs, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	rec := httptest.NewRecorder()
	var got any
	func() {
		defer func() { got = recover() }()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	}()
	if got != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want http.ErrAbortHandler re-panicked so net/http can abort the connection", got)
	}
	if rec.Code == http.StatusInternalServerError || strings.Contains(logs.String(), "panic in handler") {
		t.Fatalf("an intentional abort must not be reported as a failure: status %d logs %s", rec.Code, logs.String())
	}
}
