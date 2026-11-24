package gate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/a2a-gate/internal/auth"
	"github.com/openagentix/openagentix/services/a2a-gate/internal/rules"
)

const rulesJSON = `{"callers":[
 {"name":"bridge","serviceAccount":{"namespace":"gw","name":"bridge"},"agents":["kagent/assistant"],"userId":{"prefix":"telegram:"}},
 {"name":"schedule-x","serviceAccount":{"namespace":"sch","name":"schedule-x"},"agents":["kagent/assistant"],"userId":{"exact":"schedule:x"}},
 {"name":"kourier-b","key":true,"agents":["kagent/assistant"],"userId":{"fixed":"kourier:b"}}
]}`

const (
	tokBridge   = "aaa.bridge.sig"
	tokSchedule = "aaa.schedule.sig"
	tokExpired  = "aaa.expired.sig"
	keyKourier  = "kourier-static-key-0123456789"
	canary      = "CANARY-MESSAGE-TEXT-7731"
)

type fakeKeys map[string]string

func (f fakeKeys) Hashes() map[string]string { return f }

type fakeReviewer struct{}

func (fakeReviewer) Review(_ context.Context, token, _ string) (string, error) {
	switch token {
	case tokBridge:
		return "system:serviceaccount:gw:bridge", nil
	case tokSchedule:
		return "system:serviceaccount:sch:schedule-x", nil
	}
	return "", auth.ErrUnauthenticated
}

type seen struct {
	Method, URL string
	Header      http.Header
	Body        []byte
}

type rig struct {
	t      *testing.T
	up     *httptest.Server
	srv    *httptest.Server
	audit  *syncBuf
	mu     sync.Mutex
	seen   []seen
	handle http.HandlerFunc
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func newRig(t *testing.T, mut func(*Options)) *rig {
	t.Helper()
	r := &rig{t: t, audit: &syncBuf{}}
	r.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.seen = append(r.seen, seen{req.Method, req.URL.String(), req.Header.Clone(), b})
		r.mu.Unlock()
		if r.handle != nil {
			r.handle(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":"1","result":{}}`)
	}))
	t.Cleanup(r.up.Close)
	rl, err := rules.Parse([]byte(rulesJSON))
	if err != nil {
		t.Fatal(err)
	}
	o := Options{
		UpstreamURL: r.up.URL,
		Auth:        auth.New(auth.Options{Rules: rl, Audience: "aud", Keys: fakeKeys{"kourier-b": sum(keyKourier)}, Reviewer: fakeReviewer{}}),
		Audit:       slog.New(slog.NewJSONHandler(r.audit, nil)),
		AuditUserID: "hash",
		Salt:        []byte("test-salt"),
		SniffBytes:  1024,
		MaxBody:     1 << 20,
	}
	if mut != nil {
		mut(&o)
	}
	h, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	r.srv = httptest.NewServer(h)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) do(method, path, token string, hdr map[string]string, body string) (*http.Response, string) {
	r.t.Helper()
	req, _ := http.NewRequest(method, r.srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (r *rig) upstreamCalls() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seen(nil), r.seen...)
}

// waitLines waits until n audit lines are written: the line is written when the handler returns, which can be
// just after the client has its response.
func (r *rig) waitLines(n int) {
	r.t.Helper()
	for i := 0; i < 200; i++ {
		if strings.Count(r.audit.String(), "\n") >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("expected %d audit lines, have:\n%s", n, r.audit.String())
}

func (r *rig) auditLines() []map[string]any {
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(r.audit.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			r.t.Fatalf("audit line is not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func msg(ctx string) string {
	return `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","contextId":"` + ctx + `","role":"ROLE_USER","parts":[{"text":"` + canary + `"}]}}}`
}

const agentPath = "/agents/kagent/assistant"

func TestUnauthenticated(t *testing.T) {
	r := newRig(t, nil)
	for name, tc := range map[string]struct{ token string }{
		"no token":       {""},
		"expired":        {tokExpired},
		"garbage":        {"nope"},
		"wrong audience": {"aaa.other.sig"},
	} {
		resp, _ := r.do("POST", agentPath, tc.token, map[string]string{"X-User-Id": "telegram:1"}, msg("c1"))
		if resp.StatusCode != 401 {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
	}
	resp, _ := r.do("POST", agentPath, "", map[string]string{"Authorization": "Basic Zm9vOmJhcg=="}, msg("c1"))
	if resp.StatusCode != 401 {
		t.Errorf("basic auth: status %d", resp.StatusCode)
	}
	if n := len(r.upstreamCalls()); n != 0 {
		t.Fatalf("upstream was called %d times", n)
	}
}

func TestIdentityHeadersAreRewritten(t *testing.T) {
	r := newRig(t, nil)
	resp, _ := r.do("POST", agentPath+"?user_id=admin@kagent.dev&x=1", tokBridge, map[string]string{
		"X-User-Id": "telegram:42", "A2A-Version": "1.0", "Traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"X-Agent-Name": "x", "X-Share-Token": "y", "Cookie": "a=b", "Proxy-Authorization": "z", "Baggage": "user.id=evil",
		"X-Kagent-Insecure-Runtime-Identity": "z", "X-Forwarded-For": "6.6.6.6", "Content-Type": "application/json",
	}, msg("c1"))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	c := r.upstreamCalls()
	if len(c) != 1 {
		t.Fatalf("upstream calls = %d", len(c))
	}
	u := c[0]
	if u.URL != agentPath {
		t.Errorf("upstream URL %q: the query (and user_id with it) must be dropped", u.URL)
	}
	if got := u.Header.Values("X-User-Id"); len(got) != 1 || got[0] != "telegram:42" {
		t.Errorf("X-User-Id = %v", got)
	}
	for _, h := range []string{"Authorization", "X-Agent-Name", "X-Share-Token", "Cookie", "Proxy-Authorization", "Baggage", "X-Kagent-Insecure-Runtime-Identity"} {
		if v := u.Header.Values(h); len(v) != 0 {
			t.Errorf("%s was forwarded: %v", h, v)
		}
	}
	if u.Header.Get("A2A-Version") != "1.0" || !strings.HasPrefix(u.Header.Get("Traceparent"), "00-0af7651916cd43dd8448eb211c80319c-") {
		t.Errorf("A2A-Version and traceparent must be forwarded: %v", u.Header)
	}
	if strings.Contains(u.Header.Get("X-Forwarded-For"), "6.6.6.6") {
		t.Errorf("a forged X-Forwarded-For was forwarded")
	}
	if !strings.Contains(string(u.Body), canary) {
		t.Error("the body must reach the agent unchanged")
	}
}

func TestUserIDRules(t *testing.T) {
	r := newRig(t, nil)
	tests := []struct {
		name, token string
		hdr         map[string]string
		status      int
		upstreamID  string
	}{
		{"prefix ok", tokBridge, map[string]string{"X-User-Id": "telegram:7"}, 200, "telegram:7"},
		{"prefix wrong", tokBridge, map[string]string{"X-User-Id": "admin@kagent.dev"}, 403, ""},
		{"prefix missing", tokBridge, nil, 403, ""},
		{"exact ok", tokSchedule, map[string]string{"X-User-Id": "schedule:x"}, 200, "schedule:x"},
		{"exact for another schedule", tokSchedule, map[string]string{"X-User-Id": "schedule:y"}, 403, ""},
		{"bridge token claims a schedule id", tokBridge, map[string]string{"X-User-Id": "schedule:demo-brief"}, 403, ""},
		{"fixed overwrites", keyKourier, map[string]string{"X-User-Id": "someone-else"}, 200, "kourier:b"},
		{"fixed fills in", keyKourier, nil, 200, "kourier:b"},
	}
	for _, tc := range tests {
		before := len(r.upstreamCalls())
		resp, _ := r.do("POST", agentPath, tc.token, tc.hdr, msg("c"))
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d want %d", tc.name, resp.StatusCode, tc.status)
			continue
		}
		calls := r.upstreamCalls()
		if tc.status == 200 {
			if got := calls[len(calls)-1].Header.Get("X-User-Id"); got != tc.upstreamID {
				t.Errorf("%s: upstream X-User-Id %q want %q", tc.name, got, tc.upstreamID)
			}
		} else if len(calls) != before {
			t.Errorf("%s: a refused request reached the upstream", tc.name)
		}
	}
}

func TestAgentRule(t *testing.T) {
	r := newRig(t, nil)
	for _, p := range []string{"/agents/kagent/other", "/agents/other/assistant", "/agents/kagent"} {
		resp, _ := r.do("POST", p, tokBridge, map[string]string{"X-User-Id": "telegram:1"}, msg("c"))
		if resp.StatusCode != 403 && resp.StatusCode != 404 {
			t.Errorf("%s: status %d", p, resp.StatusCode)
		}
	}
	if len(r.upstreamCalls()) != 0 {
		t.Fatal("upstream was reached")
	}
}

func TestOnlyA2APathsPass(t *testing.T) {
	r := newRig(t, nil)
	hdr := map[string]string{"X-User-Id": "telegram:1", "Content-Type": "application/json"}
	paths := []struct{ method, path, ctype string }{
		{"POST", "/kagent.runtime.v1.MemoryService/List", "application/grpc"},
		{"POST", "/kagent.runtime.v1.MemoryService/Delete", "application/grpc-web+proto"},
		{"POST", agentPath, "application/grpc"},
		{"POST", agentPath, "application/grpc-web-text"},
		{"POST", agentPath, "Application/GRPC+proto"},
		{"GET", "/api/sessions", ""},
		{"GET", "/api/memories", ""},
		{"POST", "/api/a2a/kagent/assistant", ""},
		{"GET", agentPath, ""},
		{"DELETE", agentPath, ""},
		{"PUT", agentPath, ""},
		{"POST", agentPath + "/admin", ""},
		{"POST", agentPath + "/../../api/sessions", ""},
		{"POST", "/agents/kagent/assistant%2f..%2f..%2fapi", ""},
		{"POST", "//agents/kagent/assistant", ""},
		{"GET", "/agents/kagent/assistant/.well-known/other.json", ""},
		{"POST", "/", ""},
		{"GET", "/metrics", ""},
	}
	for _, p := range paths {
		h := map[string]string{}
		for k, v := range hdr {
			h[k] = v
		}
		if p.ctype != "" {
			h["Content-Type"] = p.ctype
		}
		resp, _ := r.do(p.method, p.path, tokBridge, h, msg("c"))
		if resp.StatusCode != 403 && resp.StatusCode != 404 {
			t.Errorf("%s %s (%s): status %d, want 403", p.method, p.path, p.ctype, resp.StatusCode)
		}
	}
	// the same refusals hold for every kind of caller
	for _, tok := range []string{tokSchedule, keyKourier} {
		resp, _ := r.do("POST", "/kagent.runtime.v1.MemoryService/List", tok, map[string]string{"Content-Type": "application/grpc"}, "")
		if resp.StatusCode != 403 {
			t.Errorf("grpc as %s: status %d", tok, resp.StatusCode)
		}
	}
	if n := len(r.upstreamCalls()); n != 0 {
		t.Fatalf("refused requests reached the upstream %d times", n)
	}
}

func TestAgentCardAndRESTSubpaths(t *testing.T) {
	r := newRig(t, nil)
	for _, p := range []string{agentPath + "/.well-known/agent-card.json", agentPath + "/.well-known/agent.json"} {
		resp, _ := r.do("GET", p, tokBridge, map[string]string{"X-User-Id": "admin@kagent.dev"}, "")
		if resp.StatusCode != 200 {
			t.Errorf("GET %s: %d", p, resp.StatusCode)
		}
	}
	c := r.upstreamCalls()
	if len(c) != 2 {
		t.Fatalf("calls = %d", len(c))
	}
	for _, u := range c {
		if len(u.Header.Values("X-User-Id")) != 0 || len(u.Header.Values("Authorization")) != 0 {
			t.Errorf("card request carried identity headers: %v", u.Header)
		}
	}
	for _, p := range []string{agentPath + "/message:send", agentPath + "/message:stream", agentPath + "/v1/message:send", agentPath + "/"} {
		resp, _ := r.do("POST", p, tokBridge, map[string]string{"X-User-Id": "telegram:1"}, msg("c"))
		if resp.StatusCode != 200 {
			t.Errorf("POST %s: %d", p, resp.StatusCode)
		}
	}
	// an agent-card GET still respects the agent rule
	if resp, _ := r.do("GET", "/agents/kagent/other/.well-known/agent-card.json", tokBridge, nil, ""); resp.StatusCode != 403 {
		t.Errorf("card of another agent: %d", resp.StatusCode)
	}
}

func TestHealthNeedsNoToken(t *testing.T) {
	r := newRig(t, nil)
	for _, p := range []string{"/healthz", "/readyz"} {
		if resp, _ := r.do("GET", p, "", nil, ""); resp.StatusCode != 200 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	if len(r.auditLines()) != 0 {
		t.Error("probes must not write audit lines")
	}
}

func TestSSEIsFlushedPerEvent(t *testing.T) {
	r := newRig(t, nil)
	release := make(chan struct{})
	r.handle = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "data: {\"n\":1}\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: {\"n\":2}\n\n")
		w.(http.Flusher).Flush()
	}
	req, _ := http.NewRequest("POST", r.srv.URL+agentPath, strings.NewReader(msg("c")))
	req.Header.Set("Authorization", "Bearer "+tokBridge)
	req.Header.Set("X-User-Id", "telegram:1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	got := make(chan string, 1)
	go func() { l, _ := rd.ReadString('\n'); got <- l }()
	select {
	case l := <-got:
		if !strings.HasPrefix(l, "data:") {
			t.Fatalf("first line %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first event not delivered before the stream ended: the proxy buffers")
	}
	close(release)
	io.Copy(io.Discard, rd)
}

func TestAuditLine(t *testing.T) {
	r := newRig(t, nil)
	r.do("POST", agentPath, tokBridge, map[string]string{
		"X-User-Id": "telegram:424242", "Traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}, msg("ctx-abc_1"))
	r.do("POST", agentPath, "bad.token.x", map[string]string{"X-User-Id": "telegram:424242"}, msg("c"))
	r.do("POST", agentPath, tokBridge, map[string]string{"X-User-Id": "telegram:424242"}, `{"params":{"message":{"contextId":"`+strings.Repeat("a", 500)+`"}}}`)
	raw := r.audit.String()
	for _, secret := range []string{canary, tokBridge, "telegram:424242", "bad.token.x"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("audit output contains %q:\n%s", secret, raw)
		}
	}
	lines := r.auditLines()
	if len(lines) != 3 {
		t.Fatalf("audit lines = %d:\n%s", len(lines), raw)
	}
	a := lines[0]
	want := map[string]any{"msg": "audit", "caller": "bridge", "agent": "kagent/assistant", "context_id": "ctx-abc_1", "trace_id": "0af7651916cd43dd8448eb211c80319c", "status": float64(200)}
	for k, v := range want {
		if a[k] != v {
			t.Errorf("%s = %v, want %v", k, a[k], v)
		}
	}
	h := sha256.Sum256(append([]byte("test-salt"), []byte("telegram:424242")...))
	if a["user_id"] != "h:"+hex.EncodeToString(h[:])[:8] {
		t.Errorf("user_id = %v", a["user_id"])
	}
	for _, k := range []string{"time", "duration_ms", "method"} {
		if _, ok := a[k]; !ok {
			t.Errorf("missing field %s", k)
		}
	}
	if lines[1]["status"] != float64(401) || lines[1]["caller"] != "" {
		t.Errorf("401 line: %v", lines[1])
	}
	if lines[2]["context_id"] != "invalid" {
		t.Errorf("an over-long contextId must be logged as invalid, got %v", lines[2]["context_id"])
	}
}

func TestAuditRawSwitch(t *testing.T) {
	r := newRig(t, func(o *Options) { o.AuditUserID = "raw" })
	r.do("POST", agentPath, tokBridge, map[string]string{"X-User-Id": "telegram:424242"}, msg("c"))
	r.waitLines(1)
	l := r.auditLines()
	if len(l) != 1 || l[0]["user_id"] != "telegram:424242" {
		t.Fatalf("raw audit: %v", l)
	}
}

func TestAuditNeverCarriesTheBody(t *testing.T) {
	r := newRig(t, nil)
	// the canary sits after the context id; and again beyond the sniff window
	big := `{"params":{"message":{"contextId":"c9","parts":[{"text":"` + canary + strings.Repeat("x", 5000) + canary + `"}]}}}`
	r.do("POST", agentPath, tokBridge, map[string]string{"X-User-Id": "telegram:1"}, big)
	r.waitLines(1)
	if strings.Contains(r.audit.String(), canary) {
		t.Fatal("message text reached the audit line")
	}
	c := r.upstreamCalls()
	if string(c[0].Body) != big {
		t.Fatalf("a body larger than the sniff window must reach the agent whole (got %d bytes, want %d)", len(c[0].Body), len(big))
	}
}

func TestContextIDBeyondSniffWindowIsNotRead(t *testing.T) {
	r := newRig(t, func(o *Options) { o.SniffBytes = 64 })
	body := `{"params":{"message":{"parts":[{"text":"` + strings.Repeat("y", 500) + `"}],"contextId":"late"}}}`
	r.do("POST", agentPath, tokBridge, map[string]string{"X-User-Id": "telegram:1"}, body)
	r.waitLines(1)
	if l := r.auditLines(); l[0]["context_id"] != "" {
		t.Fatalf("context_id = %v", l[0]["context_id"])
	}
	if got := r.upstreamCalls()[0].Body; string(got) != body {
		t.Fatal("body altered")
	}
}

func TestUpstreamDown(t *testing.T) {
	r := newRig(t, func(o *Options) { o.UpstreamURL = "http://127.0.0.1:1" })
	resp, body := r.do("POST", agentPath, tokBridge, map[string]string{"X-User-Id": "telegram:1"}, msg("c"))
	if resp.StatusCode != 502 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if strings.Contains(body, "127.0.0.1") {
		t.Errorf("the upstream address leaks into the response: %q", body)
	}
	r.waitLines(1)
	if l := r.auditLines(); l[0]["status"] != float64(502) {
		t.Errorf("audit: %v", l[0])
	}
}

func TestBodyTooLarge(t *testing.T) {
	r := newRig(t, func(o *Options) { o.MaxBody = 1000 })
	resp, _ := r.do("POST", agentPath, tokBridge, map[string]string{"X-User-Id": "telegram:1"}, strings.Repeat("a", 5000))
	if resp.StatusCode != 413 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestAuthUnavailableFailsClosed(t *testing.T) {
	rl, _ := rules.Parse([]byte(rulesJSON))
	r := newRig(t, func(o *Options) {
		o.Auth = auth.New(auth.Options{Rules: rl, Audience: "aud", Reviewer: errReviewer{}})
	})
	resp, _ := r.do("POST", agentPath, tokBridge, map[string]string{"X-User-Id": "telegram:1"}, msg("c"))
	if resp.StatusCode != 503 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

type errReviewer struct{}

func (errReviewer) Review(context.Context, string, string) (string, error) {
	return "", io.ErrUnexpectedEOF
}
