package call

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type captured struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []map[string]any
}

func (c *captured) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headers = append(c.headers, r.Header.Clone())
	c.bodies = append(c.bodies, m)
}

func task(state, answer, failText string) string {
	t := map[string]any{"id": "task-1", "contextId": "ctx-1", "status": map[string]any{"state": state}}
	if answer != "" {
		t["artifacts"] = []any{map[string]any{"parts": []any{map[string]any{"text": answer}}}}
	}
	if failText != "" {
		t["status"] = map[string]any{"state": state, "message": map[string]any{"parts": []any{map[string]any{"text": failText}}}}
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "result": map[string]any{"task": t}})
	return string(b)
}

func server(t *testing.T, c *captured, status int, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c != nil {
			c.record(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func cfg(url string) Config {
	return Config{URL: url, UserID: "schedule:test", Prompt: "hello", Timeout: 5 * time.Second}
}

func TestRun_Completed(t *testing.T) {
	s := server(t, nil, 200, task("TASK_STATE_COMPLETED", "four", ""))
	res, err := Run(context.Background(), cfg(s.URL))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != "TASK_STATE_COMPLETED" || res.TaskID != "task-1" || res.Answer != "four" {
		t.Fatalf("res = %+v", res)
	}
	if res.Duration <= 0 {
		t.Fatalf("duration = %v", res.Duration)
	}
}

func TestRun_RequestShape(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "x", ""))
	if _, err := Run(context.Background(), cfg(s.URL)); err != nil {
		t.Fatal(err)
	}
	h := c.headers[0]
	if h.Get("X-User-Id") != "schedule:test" {
		t.Errorf("X-User-Id = %q", h.Get("X-User-Id"))
	}
	if h.Get("A2A-Version") != "1.0" {
		t.Errorf("A2A-Version = %q", h.Get("A2A-Version"))
	}
	if h.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", h.Get("Content-Type"))
	}
	b := c.bodies[0]
	if b["jsonrpc"] != "2.0" || b["method"] != "SendMessage" {
		t.Errorf("envelope = %v", b)
	}
	msg := b["params"].(map[string]any)["message"].(map[string]any)
	if msg["role"] != "ROLE_USER" {
		t.Errorf("role = %v", msg["role"])
	}
	if _, has := msg["contextId"]; has {
		t.Errorf("a new conversation must not send contextId: %v", msg)
	}
	if id, _ := msg["messageId"].(string); len(id) < 16 {
		t.Errorf("messageId = %q", id)
	}
	parts := msg["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "hello" {
		t.Errorf("parts = %v", parts)
	}
}

func TestRun_NoUserIDHeaderWhenEmpty(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "x", ""))
	cf := cfg(s.URL)
	cf.UserID = ""
	if _, err := Run(context.Background(), cf); err == nil {
		t.Fatal("an empty user id must be rejected: a schedule without identity would fail on memory agents")
	}
	if len(c.headers) != 0 {
		t.Fatal("no request should have been sent")
	}
}

func TestRun_MessageIDsAreUnique(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "x", ""))
	for i := 0; i < 20; i++ {
		if _, err := Run(context.Background(), cfg(s.URL)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, b := range c.bodies {
		id := b["params"].(map[string]any)["message"].(map[string]any)["messageId"].(string)
		if seen[id] {
			t.Fatalf("duplicate messageId %s", id)
		}
		seen[id] = true
	}
}

func TestRun_NotCompleted(t *testing.T) {
	for _, st := range []string{"TASK_STATE_FAILED", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED", "TASK_STATE_WORKING", "TASK_STATE_SUBMITTED"} {
		s := server(t, nil, 200, task(st, "", "agent run failed: boom"))
		res, err := Run(context.Background(), cfg(s.URL))
		var ne *NotCompletedError
		if !errors.As(err, &ne) {
			t.Fatalf("%s: err = %v", st, err)
		}
		if res.State != st || res.TaskID != "task-1" {
			t.Errorf("%s: res = %+v", st, res)
		}
		if !strings.Contains(err.Error(), st) || !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s: message lacks state or detail: %v", st, err)
		}
	}
}

func TestRun_FailureDetailIsTruncated(t *testing.T) {
	s := server(t, nil, 200, task("TASK_STATE_FAILED", "", strings.Repeat("x", 5000)))
	_, err := Run(context.Background(), cfg(s.URL))
	if err == nil || len(err.Error()) > 600 {
		t.Fatalf("err len = %d", len(err.Error()))
	}
}

func TestRun_HTTPErrors(t *testing.T) {
	for _, code := range []int{400, 403, 404, 500, 503, 302} {
		s := server(t, nil, code, `{"error":"nope"}`)
		_, err := Run(context.Background(), cfg(s.URL))
		var he *HTTPError
		if !errors.As(err, &he) || he.Status != code {
			t.Fatalf("%d: err = %v", code, err)
		}
	}
}

func TestRun_DoesNotFollowRedirects(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	_, err := Run(context.Background(), cfg(s.URL))
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 307 || hits != 0 {
		t.Fatalf("err = %v hits = %d", err, hits)
	}
}

func TestRun_JSONRPCError(t *testing.T) {
	s := server(t, nil, 200, `{"jsonrpc":"2.0","id":"1","error":{"code":-32601,"message":"method not found"}}`)
	_, err := Run(context.Background(), cfg(s.URL))
	var re *RPCError
	if !errors.As(err, &re) || re.Code != -32601 || !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_Malformed(t *testing.T) {
	for _, body := range []string{"not json", "", `{"jsonrpc":"2.0","id":"1","result":{}}`, `{"jsonrpc":"2.0","id":"1","result":{"message":{"parts":[{"text":"hi"}]}}}`} {
		s := server(t, nil, 200, body)
		_, err := Run(context.Background(), cfg(s.URL))
		if err == nil {
			t.Fatalf("body %q: want an error (no task in the response)", body)
		}
	}
}

func TestRun_OversizedResponse(t *testing.T) {
	s := server(t, nil, 200, task("TASK_STATE_COMPLETED", strings.Repeat("a", 2000), ""))
	cf := cfg(s.URL)
	cf.MaxResponse = 1000
	_, err := Run(context.Background(), cf)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_ResponseExactlyAtLimitIsAccepted(t *testing.T) {
	body := task("TASK_STATE_COMPLETED", "a", "")
	s := server(t, nil, 200, body)
	cf := cfg(s.URL)
	cf.MaxResponse = int64(len(body))
	if _, err := Run(context.Background(), cf); err != nil {
		t.Fatal(err)
	}
}

func TestRun_Timeout(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	defer close(release)
	cf := cfg(s.URL)
	cf.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := Run(context.Background(), cf)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestRun_ConnectionRefused(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := s.URL
	s.Close()
	if _, err := Run(context.Background(), cfg(url)); err == nil || errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
}

func TestRun_AnswerJoinsParts(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"parts":[{"text":"a"},{"text":"b"}]},{"parts":[{"text":"c"}]}]}}}`
	s := server(t, nil, 200, body)
	res, err := Run(context.Background(), cfg(s.URL))
	if err != nil || res.Answer != "a\nb\nc" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestRun_Validation(t *testing.T) {
	for name, mod := range map[string]func(*Config){
		"url":     func(c *Config) { c.URL = "" },
		"prompt":  func(c *Config) { c.Prompt = "  \n" },
		"timeout": func(c *Config) { c.Timeout = 0 },
	} {
		c := cfg("http://127.0.0.1:1")
		mod(&c)
		if _, err := Run(context.Background(), c); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestRun_TokenFileIsReadForTheRequest(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "ok", ""))
	f := t.TempDir() + "/token"
	if err := writeFile(f, "projected-token\n"); err != nil {
		t.Fatal(err)
	}
	cf := cfg(s.URL)
	cf.TokenFile = f
	if _, err := Run(context.Background(), cf); err != nil {
		t.Fatal(err)
	}
	if got := c.headers[0].Get("Authorization"); got != "Bearer projected-token" {
		t.Fatalf("Authorization = %q", got)
	}
	// rotated between two calls: the second call sees the new value
	_ = writeFile(f, "rotated-token")
	if _, err := Run(context.Background(), cf); err != nil {
		t.Fatal(err)
	}
	if got := c.headers[1].Get("Authorization"); got != "Bearer rotated-token" {
		t.Fatalf("Authorization after rotation = %q", got)
	}
}

func TestRun_NoTokenFileSendsNoAuthorization(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "ok", ""))
	if _, err := Run(context.Background(), cfg(s.URL)); err != nil {
		t.Fatal(err)
	}
	if v := c.headers[0].Values("Authorization"); len(v) != 0 {
		t.Fatalf("Authorization = %v", v)
	}
}

func TestRun_MissingOrEmptyTokenFileIsAClearErrorAndNothingIsSent(t *testing.T) {
	var c captured
	s := server(t, &c, 200, task("TASK_STATE_COMPLETED", "ok", ""))
	dir := t.TempDir()
	cf := cfg(s.URL)
	cf.TokenFile = dir + "/missing"
	_, err := Run(context.Background(), cf)
	if err == nil || !strings.Contains(err.Error(), "A2A_TOKEN_FILE") {
		t.Fatalf("missing: %v", err)
	}
	_ = writeFile(dir+"/empty", " \n")
	cf.TokenFile = dir + "/empty"
	if _, err = Run(context.Background(), cf); err == nil || !strings.Contains(err.Error(), "A2A_TOKEN_FILE") {
		t.Fatalf("empty: %v", err)
	}
	if len(c.headers) != 0 {
		t.Fatal("a call without its token must not be sent")
	}
}

func writeFile(path, s string) error { return os.WriteFile(path, []byte(s), 0o600) }
