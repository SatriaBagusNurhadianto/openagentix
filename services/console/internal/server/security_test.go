package server_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/console/ext"
	"github.com/openagentix/openagentix/services/console/internal/backend"
	"github.com/openagentix/openagentix/services/console/internal/builtin"
	"github.com/openagentix/openagentix/services/console/internal/model"
	"github.com/openagentix/openagentix/services/console/internal/server"
)

// clock is the fake clock of the throttle tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)} }
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func withOpts(f func(*server.Options)) func(*rigOpt) { return func(o *rigOpt) { o.mod = f } }

// --- I3: Host allowlist, Origin, throttling ---

func TestHostAllowlistAnswers421BeforeRouting(t *testing.T) {
	r := newRig(t, withOpts(func(o *server.Options) { o.AllowedHosts = []string{"console.example.com", "ingress.internal:8443"} }))
	good := []string{"localhost", "localhost:8080", "LOCALHOST:1", "127.0.0.1", "127.0.0.1:9999", "[::1]", "[::1]:8080", "console.example.com", "console.example.com:443", "ingress.internal:8443"}
	bad := []string{"evil.example", "evil.example:8080", "localhost.evil.example", "127.0.0.1.evil.example", "console.example.com.evil.com", "ingress.internal:9",
		"ingress.internal", "10.1.2.3:8080", "[::2]", "localhost@evil.example", "evil.example#localhost", "localhost:evil", "localhost:99999999"}
	for _, p := range []string{"/healthz", "/", "/api/v1/session", "/api/v1/agents/x", "/nope.js"} {
		for _, h := range good {
			resp, _ := r.do(t, "GET", p, nil, "Host", h)
			if resp.StatusCode == 421 {
				t.Errorf("Host %q on %s was refused", h, p)
			}
		}
		for _, h := range bad {
			n := len(r.api.requests())
			resp, b := r.do(t, "GET", p, nil, "Host", h)
			if len(r.api.requests()) != n {
				t.Errorf("Host %q on %s reached the cluster", h, p)
			}
			if resp.StatusCode != 421 || len(b) != 0 {
				t.Errorf("Host %q on %s: %s body=%q", h, p, resp.Status, b)
			}
			if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("Host %q: the refusal lacks the security headers", h)
			}
		}
	}
	if resp, _ := r.do(t, "POST", "/api/v1/agents", map[string]any{"name": "x", "spec": spec("m")}, "Host", "evil.example"); resp.StatusCode != 421 {
		t.Errorf("POST with a foreign host: %s", resp.Status)
	}
}

func TestOriginOnAnyRequestMustMatchTheHost(t *testing.T) {
	r := newRig(t, withOpts(func(o *server.Options) { o.AllowedHosts = []string{"console.example.com"} }))
	host := strings.TrimPrefix(r.ts.URL, "http://")
	for _, p := range []string{"/healthz", "/api/v1/session", "/"} {
		for _, org := range []string{"https://evil.example", "null", "http://localhost:1234", "http://127.0.0.1", "http://" + host + ".evil.example", "http://console.example.com", "://bad"} {
			resp, _ := r.do(t, "GET", p, nil, "Origin", org)
			if resp.StatusCode != 403 {
				t.Errorf("GET %s with Origin %q: %s (the Origin must equal the Host)", p, org, resp.Status)
			}
		}
		if resp, _ := r.do(t, "GET", p, nil, "Origin", "http://"+host); resp.StatusCode != 200 {
			t.Errorf("GET %s with the page's own origin: %s", p, resp.Status)
		}
	}
	// the DNS-rebinding case: the page's name is the attacker's, and Origin and Host agree
	resp, _ := r.do(t, "GET", "/api/v1/session", nil, "Host", "evil.example", "Origin", "http://evil.example")
	if resp.StatusCode != 421 {
		t.Errorf("rebinding: %s", resp.Status)
	}
	// a configured name works with its own origin, and only with it
	if resp, _ := r.do(t, "GET", "/api/v1/session", nil, "Host", "console.example.com", "Origin", "https://console.example.com"); resp.StatusCode != 200 {
		t.Errorf("configured host: %s", resp.Status)
	}
	if resp, _ := r.do(t, "GET", "/api/v1/session", nil, "Host", "console.example.com", "Origin", "https://localhost"); resp.StatusCode != 403 {
		t.Errorf("configured host with another allowed origin: %s", resp.Status)
	}
}

func TestFailedLoginsAreThrottledWithGrowingBackoff(t *testing.T) {
	clk := newClock()
	r := newRig(t, withOpts(func(o *server.Options) { o.AuthFailLimit, o.AuthFailLimitGlobal, o.Now = 3, 1000, clk.Now }))
	bad := func() *http.Response {
		resp, _ := r.do(t, "GET", "/api/v1/session", nil, "Authorization", "Bearer wrong")
		return resp
	}
	good := func() *http.Response { resp, _ := r.do(t, "GET", "/api/v1/session", nil); return resp }
	for i := 0; i < 20; i++ { // successes cost nothing
		if good().StatusCode != 200 {
			t.Fatal("a valid request was refused")
		}
	}
	for i := 0; i < 3; i++ {
		if c := bad().StatusCode; c != 401 {
			t.Fatalf("failure %d: %d", i+1, c)
		}
	}
	resp := bad()
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "60" {
		t.Fatalf("after 3 failures: %s Retry-After=%q", resp.Status, resp.Header.Get("Retry-After"))
	}
	// during the back-off even the right token waits: a lock-out that a correct guess lifts would be no lock-out
	if c := good().StatusCode; c != 429 {
		t.Fatalf("the right token during the back-off: %d", c)
	}
	_, b := r.do(t, "GET", "/api/v1/session", nil, "Authorization", "Bearer wrong")
	if errCode(t, b) != "too_many_attempts" {
		t.Fatal(string(b))
	}
	clk.Add(30 * time.Second)
	if ra := bad().Header.Get("Retry-After"); ra != "30" {
		t.Fatalf("Retry-After counts down: %q", ra)
	}
	clk.Add(31 * time.Second)
	if c := good().StatusCode; c != 200 {
		t.Fatalf("after the back-off: %d", c)
	}
	// a second round backs off for longer
	for i := 0; i < 3; i++ {
		bad()
	}
	if ra := bad().Header.Get("Retry-After"); ra != "120" {
		t.Fatalf("second back-off: Retry-After=%q", ra)
	}
	// the other routes of the API are covered too, and the app shell and /healthz never are
	for _, p := range []string{"/healthz", "/", "/agents/x"} {
		if resp, _ := r.do(t, "GET", p, nil, "Authorization", ""); resp.StatusCode != 200 {
			t.Errorf("%s during a back-off: %s", p, resp.Status)
		}
	}
}

func TestFailuresOutsideTheWindowDoNotAdd(t *testing.T) {
	clk := newClock()
	r := newRig(t, withOpts(func(o *server.Options) { o.AuthFailLimit, o.AuthFailLimitGlobal, o.Now = 3, 1000, clk.Now }))
	for round := 0; round < 4; round++ {
		for i := 0; i < 2; i++ {
			if resp, _ := r.do(t, "GET", "/api/v1/session", nil, "Authorization", "Bearer wrong"); resp.StatusCode != 401 {
				t.Fatalf("round %d: %s", round, resp.Status)
			}
		}
		clk.Add(61 * time.Second)
	}
}

func TestGlobalFailureCapAppliesToEveryAddress(t *testing.T) {
	clk := newClock()
	r := newRig(t, withOpts(func(o *server.Options) { o.AuthFailLimit, o.AuthFailLimitGlobal, o.Now = 1000, 4, clk.Now }))
	for i := 0; i < 4; i++ {
		r.do(t, "GET", "/api/v1/session", nil, "Authorization", "Bearer wrong")
	}
	resp, _ := r.do(t, "GET", "/api/v1/session", nil)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("%s", resp.Status)
	}
}

func TestValidateIsRateLimitedPerIdentity(t *testing.T) {
	clk := newClock()
	r := newRig(t, withOpts(func(o *server.Options) { o.ValidatePerMinute, o.Now = 3, clk.Now }))
	body := map[string]any{"name": "", "spec": spec("claude-haiku")}
	for i := 0; i < 3; i++ {
		if resp, b := r.do(t, "POST", "/api/v1/agents/validate", body); resp.StatusCode != 200 {
			t.Fatal(i, resp.Status, string(b))
		}
	}
	resp, b := r.do(t, "POST", "/api/v1/agents/validate", body)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || errCode(t, b) != "rate_limited" {
		t.Fatal(resp.Status, resp.Header, string(b))
	}
	// other routes are not limited by it
	if resp, _ := r.do(t, "GET", "/api/v1/agents", nil); resp.StatusCode != 200 {
		t.Error(resp.Status)
	}
	clk.Add(61 * time.Second)
	if resp, _ := r.do(t, "POST", "/api/v1/agents/validate", body); resp.StatusCode != 200 {
		t.Error("the window did not reset:", resp.Status)
	}
}

func TestViewerValidateSendsNoDryRunWrites(t *testing.T) {
	r := newRig(t, func(o *rigOpt) { o.viewer = true })
	r.api.seed("researcher", spec("claude-haiku"), map[string]any{})
	for _, name := range []string{"researcher", "draft", ""} {
		resp, b := r.do(t, "POST", "/api/v1/agents/validate", map[string]any{"name": name, "spec": spec("gpt-x")})
		if resp.StatusCode != 200 {
			t.Fatal(resp.Status, string(b))
		}
		v := decodeInto[model.ValidateResult](t, b)
		if !v.Schema.Skipped || len(v.Predicted) == 0 {
			t.Errorf("%q: a viewer gets predictions and no schema check: %+v", name, v)
		}
	}
	for _, q := range r.api.requests() {
		if q.Method != "GET" {
			t.Errorf("a viewer's validate reached the cluster as %s %s?%s", q.Method, q.Path, q.Query)
		}
	}
}

// --- I6: extension points ---

type recAuthz struct {
	mu    sync.Mutex
	calls []struct {
		A ext.Action
		R ext.Resource
	}
	deny func(ext.Action, ext.Resource) bool
}

func (a *recAuthz) Authorize(_ context.Context, _ ext.Identity, act ext.Action, res ext.Resource) ext.Decision {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, struct {
		A ext.Action
		R ext.Resource
	}{act, res})
	if a.deny != nil && a.deny(act, res) {
		return ext.Decision{Reason: "Not for you."}
	}
	return ext.Decision{Allowed: true}
}

func (a *recAuthz) of(act ext.Action) []ext.Resource {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []ext.Resource
	for _, c := range a.calls {
		if c.A == act {
			out = append(out, c.R)
		}
	}
	return out
}

func TestAuthorizerIsAskedAgainAfterTheBodyIsDecoded(t *testing.T) {
	az := &recAuthz{deny: func(a ext.Action, r ext.Resource) bool { return r.Spec != nil && r.Spec.Model == "forbidden-model" }}
	r := newRig(t, withOpts(func(o *server.Options) { o.Authorizer = az }))
	s := spec("claude-haiku")
	if resp, b := r.do(t, "POST", "/api/v1/agents", map[string]any{"name": "fresh", "spec": s}); resp.StatusCode != 201 {
		t.Fatal(resp.Status, string(b))
	}
	cr := az.of(ext.ActionCreate)
	if len(cr) != 2 || cr[0].Name != "" || cr[0].Spec != nil {
		t.Fatalf("the first question is bare: %+v", cr)
	}
	if cr[1].Name != "fresh" || cr[1].Spec == nil || cr[1].Spec.Model != "claude-haiku" || cr[1].Spec.ToolProfile != "lookup" {
		t.Fatalf("the second question names the agent and its spec: %+v %+v", cr[1], cr[1].Spec)
	}
	// a refusal on the spec: 403 with the Authorizer's own sentence, nothing written
	before := len(r.api.defs)
	resp, b := r.do(t, "POST", "/api/v1/agents", map[string]any{"name": "other", "spec": spec("forbidden-model")})
	if resp.StatusCode != 403 || !strings.Contains(string(b), "Not for you.") || len(r.api.defs) != before {
		t.Fatal(resp.Status, string(b), len(r.api.defs))
	}
	if !strings.Contains(r.audit.String(), `"outcome":"denied"`) {
		t.Error("the refusal was not audited as denied")
	}
	// update: the stored labels are in the summary
	r.api.seed("labelled", spec("claude-haiku"), map[string]any{})
	r.api.defs["labelled"]["metadata"].(map[string]any)["labels"] = map[string]any{"openagentix.io/team": "platform", "openagentix.io/project": "demo"}
	rv := r.api.defs["labelled"]["metadata"].(map[string]any)["resourceVersion"]
	if resp, b := r.do(t, "PUT", "/api/v1/agents/labelled", map[string]any{"resourceVersion": rv, "spec": spec("claude-haiku")}); resp.StatusCode != 200 {
		t.Fatal(resp.Status, string(b))
	}
	up := az.of(ext.ActionUpdate)
	if len(up) != 2 || up[0].Name != "labelled" || up[0].Spec != nil {
		t.Fatalf("update, first question: %+v", up)
	}
	if got := up[1].Spec; got == nil || got.Team != "platform" || got.Project != "demo" || got.Model != "claude-haiku" {
		t.Fatalf("update, second question: %+v", got)
	}
	// a refused update never reaches the cluster
	n := len(r.api.requests())
	resp, _ = r.do(t, "PUT", "/api/v1/agents/labelled", map[string]any{"resourceVersion": "9", "spec": spec("forbidden-model")})
	if resp.StatusCode != 403 {
		t.Fatal(resp.Status)
	}
	for _, q := range r.api.requests()[n:] {
		if q.Method != "GET" {
			t.Errorf("a refused update reached the cluster: %s %s", q.Method, q.Path)
		}
	}
}

func TestSessionDoesNotPromiseWhatTheAuthorizerWouldRefuse(t *testing.T) {
	az := &recAuthz{deny: func(a ext.Action, r ext.Resource) bool { return a == ext.ActionUpdate || a == ext.ActionDelete }}
	r := newRig(t, withOpts(func(o *server.Options) { o.Authorizer = az }))
	_, b := r.do(t, "GET", "/api/v1/session", nil)
	s := decodeInto[struct {
		Role         string
		CanWrite     bool
		Capabilities map[string]bool
	}](t, b)
	want := map[string]bool{"create": true, "update": false, "delete": false, "validate": true}
	for k, v := range want {
		if s.Capabilities[k] != v {
			t.Errorf("capabilities %v, want %v", s.Capabilities, want)
			break
		}
	}
	if s.CanWrite || s.Role != "viewer" {
		t.Errorf("canWrite promises more than update and delete allow: %+v", s)
	}
}

type testSink struct {
	mu        sync.Mutex
	recorded  []ext.AuditEvent
	recordErr error
	panicRec  bool
	pre       func(ext.AuditEvent) error
	preCalls  []ext.AuditEvent
}

func (s *testSink) Record(_ context.Context, e ext.AuditEvent) error {
	s.mu.Lock()
	s.recorded = append(s.recorded, e)
	s.mu.Unlock()
	if s.panicRec {
		panic("sink exploded")
	}
	return s.recordErr
}

type preSink struct{ *testSink }

func (s preSink) PreWrite(_ context.Context, e ext.AuditEvent) error {
	s.mu.Lock()
	s.preCalls = append(s.preCalls, e)
	s.mu.Unlock()
	if s.pre != nil {
		return s.pre(e)
	}
	return nil
}

func TestAuditSinkFailureNeverChangesTheResponse(t *testing.T) {
	for name, sink := range map[string]*testSink{"panic": {panicRec: true}, "error": {recordErr: errors.New("disk full CANARY-SINK")}} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, withOpts(func(o *server.Options) { o.Audit = sink }))
			resp, b := r.do(t, "POST", "/api/v1/agents", map[string]any{"name": "fresh", "spec": spec("claude-haiku")})
			if resp.StatusCode != 201 || strings.Contains(string(b), "CANARY-SINK") || strings.Contains(string(b), "exploded") {
				t.Fatal(resp.Status, string(b))
			}
			if _, ok := r.api.defs["fresh"]; !ok {
				t.Fatal("the write did not happen")
			}
			if resp, _ := r.do(t, "DELETE", "/api/v1/agents/fresh", nil); resp.StatusCode != 202 {
				t.Fatal(resp.Status)
			}
			if !strings.Contains(r.logs.String(), "audit") {
				t.Errorf("the failure of the sink is not in the log: %s", r.logs.String())
			}
			if len(sink.recorded) != 2 {
				t.Errorf("recorded %d events", len(sink.recorded))
			}
		})
	}
}

func TestPreWriteCanRefuseAWriteAndFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		pre  func(ext.AuditEvent) error
		code int
		ec   string
	}{
		{"refused", func(ext.AuditEvent) error { return fmt.Errorf("policy says no: %w", ext.ErrRefused) }, 403, "forbidden"},
		{"unavailable", func(ext.AuditEvent) error { return errors.New("audit store down CANARY-PRE") }, 503, "audit_unavailable"},
		{"panic", func(ext.AuditEvent) error { panic("boom") }, 503, "audit_unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := preSink{&testSink{pre: c.pre}}
			r := newRig(t, withOpts(func(o *server.Options) { o.Audit = sink }))
			r.api.seed("old", spec("claude-haiku"), map[string]any{})
			rv := r.api.defs["old"]["metadata"].(map[string]any)["resourceVersion"]
			n := len(r.api.requests())
			for _, m := range []struct {
				method, path string
				body         any
			}{{"POST", "/api/v1/agents", map[string]any{"name": "fresh", "spec": spec("claude-haiku")}},
				{"PUT", "/api/v1/agents/old", map[string]any{"resourceVersion": rv, "spec": spec("claude-haiku")}}, {"DELETE", "/api/v1/agents/old", nil}} {
				resp, b := r.do(t, m.method, m.path, m.body)
				if resp.StatusCode != c.code || errCode(t, b) != c.ec || strings.Contains(string(b), "CANARY") || strings.Contains(string(b), "boom") {
					t.Errorf("%s %s: %s %s", m.method, m.path, resp.Status, b)
				}
			}
			for _, q := range r.api.requests()[n:] {
				if q.Method == "POST" || q.Method == "PUT" || q.Method == "DELETE" {
					t.Errorf("a write got through a refused PreWrite: %s %s", q.Method, q.Path)
				}
			}
			if _, ok := r.api.defs["old"]; !ok || len(r.api.defs) != 1 {
				t.Error("the cluster changed")
			}
			if len(sink.preCalls) != 3 || sink.preCalls[0].Outcome != "pending" || sink.preCalls[0].Status != 0 {
				t.Errorf("PreWrite calls: %+v", sink.preCalls)
			}
		})
	}
}

func TestPreWriteIsNotCalledForReadsOrDeniedWrites(t *testing.T) {
	sink := preSink{&testSink{}}
	r := newRig(t, func(o *rigOpt) {
		o.viewer = true
		o.mod = func(op *server.Options) { op.Audit = sink }
	})
	r.do(t, "GET", "/api/v1/agents", nil)
	r.do(t, "POST", "/api/v1/agents/validate", map[string]any{"name": "x", "spec": spec("m")})
	r.do(t, "DELETE", "/api/v1/agents/x", nil)
	if len(sink.preCalls) != 0 {
		t.Fatalf("%+v", sink.preCalls)
	}
	var _ ext.PreWriter = sink
	if _, ok := any(builtin.LogAudit{}).(ext.PreWriter); ok {
		t.Error("the built-in sink must not use PreWrite")
	}
}

// --- M1: upstream error text stays upstream ---

const canary = "CANARY-UPSTREAM-TEXT-9d1"

func upstreamStatus(code int, reason, msg string, causes ...map[string]string) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/agentdefinitions") && !strings.Contains(r.URL.Path, "/agentdefinitions/") {
			return false
		}
		if r.Method == http.MethodGet && r.URL.Path != "/apis/openagentix.io/v1alpha1/namespaces/kagent/agentdefinitions" {
			return false
		}
		status(w, code, reason, msg, causes...)
		return true
	}
}

func TestUpstreamErrorTextIsNeverEchoed(t *testing.T) {
	webhook := `admission webhook "validate.` + canary + `.example" denied the request: ` + canary
	cases := []struct {
		name   string
		up     func(http.ResponseWriter, *http.Request) bool
		code   int
		ecode  string
		mustIn string
	}{
		{"invalid", upstreamStatus(422, "Invalid", canary, map[string]string{"field": "spec.instructions", "message": canary, "reason": "FieldValueRequired"}), 422, "invalid", "spec.instructions"},
		{"invalid with a hostile field", upstreamStatus(422, "Invalid", canary, map[string]string{"field": "spec.<script>" + canary, "message": canary}), 422, "invalid", ""},
		{"forbidden", upstreamStatus(403, "Forbidden", `agentdefinitions is forbidden: User "system:serviceaccount:x" `+canary), 502, "cluster_forbidden", "ServiceAccount"},
		{"quota", upstreamStatus(403, "Forbidden", "exceeded quota: "+canary), 422, "quota_exceeded", "quota"},
		{"webhook 403", upstreamStatus(403, "Forbidden", webhook), 422, "admission_denied", "admission policy"},
		{"webhook 400", upstreamStatus(400, "BadRequest", webhook), 422, "admission_denied", "admission policy"},
		{"validating admission policy", upstreamStatus(422, "Invalid", "ValidatingAdmissionPolicy 'p"+canary+"' with binding 'b' denied request: "+canary), 422, "admission_denied", "admission policy"},
		{"conflict", upstreamStatus(409, "Conflict", canary), 409, "conflict", "changed by someone else"},
		{"server error", upstreamStatus(500, "InternalError", canary), 502, "cluster_error", "request id"},
		{"unknown 418", upstreamStatus(418, "Teapot", canary), 502, "cluster_error", "request id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.api.override = func(w http.ResponseWriter, q *http.Request) bool {
				if q.Method == http.MethodPost && strings.HasSuffix(q.URL.Path, "/agentdefinitions") {
					return c.up(w, q)
				}
				return false
			}
			resp, b := r.do(t, "POST", "/api/v1/agents", map[string]any{"name": "fresh", "spec": spec("claude-haiku")})
			if resp.StatusCode != c.code || errCode(t, b) != c.ecode {
				t.Fatalf("%s %s", resp.Status, b)
			}
			if strings.Contains(string(b), canary) || strings.Contains(string(b), "<script>") {
				t.Fatalf("upstream text reached the browser: %s", b)
			}
			if !strings.Contains(strings.ToLower(string(b)), strings.ToLower(c.mustIn)) {
				t.Errorf("want %q in %s", c.mustIn, b)
			}
			if c.ecode == "cluster_error" {
				rid := resp.Header.Get("X-Request-Id")
				if rid == "" || !strings.Contains(string(b), rid) || !strings.Contains(r.logs.String(), rid) {
					t.Errorf("the request id %q is not in both the answer and the log\nanswer: %s\nlog: %s", rid, b, r.logs.String())
				}
			}
		})
	}
}

func TestUpstreamErrorTextOnReadAndValidate(t *testing.T) {
	r := newRig(t)
	r.api.seed("researcher", spec("claude-haiku"), map[string]any{})
	r.api.override = func(w http.ResponseWriter, q *http.Request) bool {
		if q.URL.Query().Get("dryRun") == "All" {
			status(w, 422, "Invalid", canary, map[string]string{"field": "spec.model", "message": canary, "reason": "FieldValueInvalid"})
			return true
		}
		if q.URL.Path == "/apis/openagentix.io/v1alpha1/namespaces/kagent/agentdefinitions/ghost" {
			status(w, 404, "NotFound", canary)
			return true
		}
		return false
	}
	resp, b := r.do(t, "POST", "/api/v1/agents/validate", map[string]any{"name": "draft", "spec": spec("claude-haiku")})
	v := decodeInto[model.ValidateResult](t, b)
	if resp.StatusCode != 200 || v.Schema.OK || len(v.Schema.Errors) == 0 || strings.Contains(string(b), canary) || !strings.Contains(strings.Join(v.Schema.Errors, " "), "spec.model") {
		t.Fatal(resp.Status, string(b))
	}
	resp, b = r.do(t, "GET", "/api/v1/agents/ghost", nil)
	if resp.StatusCode == 200 || strings.Contains(string(b), canary) {
		t.Fatal(resp.Status, string(b))
	}
	r.api.override = upstreamStatus(500, "InternalError", canary)
	resp, b = r.do(t, "GET", "/api/v1/agents", nil)
	if resp.StatusCode != 502 || strings.Contains(string(b), canary) {
		t.Fatal(resp.Status, string(b))
	}
}

// --- M2: the save keeps what the console does not know ---

func rawSpec(r *rig, name string) map[string]any {
	return r.api.defs[name]["spec"].(map[string]any)
}

func TestUpdateKeepsFieldsTheConsoleDoesNotKnow(t *testing.T) {
	r := newRig(t)
	sp := spec("claude-haiku")
	sp["futureField"] = map[string]any{"a": 1.0, "b": []any{"x"}}
	sp["description"] = "old"
	r.api.seed("keeper", sp, map[string]any{})
	_, b := r.do(t, "GET", "/api/v1/agents/keeper", nil)
	d := decodeInto[model.Detail](t, b)
	if !slices.Equal(d.Unmanaged, []string{"futureField"}) {
		t.Fatalf("unmanaged = %v", d.Unmanaged)
	}
	next := spec("claude-sonnet")
	next["toolProfile"] = ""
	delete(next, "tools")
	resp, b := r.do(t, "PUT", "/api/v1/agents/keeper", map[string]any{"resourceVersion": d.ResourceVersion, "spec": next})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, string(b))
	}
	got := rawSpec(r, "keeper")
	if got["model"] != "claude-sonnet" || got["futureField"] == nil || got["futureField"].(map[string]any)["a"] != 1.0 {
		t.Fatalf("saved spec: %v", got)
	}
	for _, k := range []string{"description", "toolProfile", "tools"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was cleared in the editor and must be gone from the object: %v", k, got)
		}
	}
	// the finalizer and other metadata are kept as before
	if f, _ := r.api.defs["keeper"]["metadata"].(map[string]any)["finalizers"].([]any); len(f) != 1 {
		t.Error("finalizers lost")
	}
}

func TestUpdateOfAPromptFromAConfigMapKeepsTheReferenceAndSendsNoInstructions(t *testing.T) {
	r := newRig(t)
	sp := map[string]any{"model": "claude-haiku", "instructionsFrom": map[string]any{"configMap": map[string]any{"name": "prompts", "key": "a"}}}
	r.api.seed("fromcm", sp, map[string]any{})
	_, b := r.do(t, "GET", "/api/v1/agents/fromcm", nil)
	d := decodeInto[model.Detail](t, b)
	if !slices.Equal(d.Unmanaged, []string{"instructionsFrom"}) {
		t.Fatalf("unmanaged = %v", d.Unmanaged)
	}
	resp, b := r.do(t, "PUT", "/api/v1/agents/fromcm", map[string]any{"resourceVersion": d.ResourceVersion, "spec": map[string]any{"model": "claude-sonnet", "instructions": ""}})
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status, string(b))
	}
	got := rawSpec(r, "fromcm")
	if got["instructionsFrom"] == nil {
		t.Fatalf("the reference was dropped: %v", got)
	}
	if _, ok := got["instructions"]; ok {
		t.Fatalf("an empty instructions field was sent next to instructionsFrom: %v", got)
	}
	// instructions typed next to the reference: refused here, with a sentence
	rv := r.api.defs["fromcm"]["metadata"].(map[string]any)["resourceVersion"]
	resp, b = r.do(t, "PUT", "/api/v1/agents/fromcm", map[string]any{"resourceVersion": rv, "spec": map[string]any{"model": "m", "instructions": "typed"}})
	if resp.StatusCode != 422 || errCode(t, b) != "invalid" || !strings.Contains(string(b), "ConfigMap") {
		t.Fatal(resp.Status, string(b))
	}
}

func TestValidateOfAnExistingNameKeepsUnknownFieldsToo(t *testing.T) {
	r := newRig(t)
	sp := spec("claude-haiku")
	sp["futureField"] = "x"
	r.api.seed("keeper", sp, map[string]any{})
	var sent map[string]any
	r.api.override = func(w http.ResponseWriter, q *http.Request) bool {
		if q.Method == "PUT" {
			_ = decodeBody(q, &sent)
		}
		return false
	}
	r.do(t, "POST", "/api/v1/agents/validate", map[string]any{"name": "keeper", "spec": spec("claude-haiku")})
	if sent == nil || sent["spec"].(map[string]any)["futureField"] != "x" {
		t.Fatalf("the dry run dropped a field: %v", sent)
	}
}

// --- M3: status.controller is opaque ---

func TestNothingReadsStatusController(t *testing.T) {
	r := newRig(t)
	r.api.seed("ctl", spec("claude-haiku"), map[string]any{"controller": map[string]any{"keySecret": "named-secret-canary", "anything": 1.0}})
	_, b := r.do(t, "GET", "/api/v1/agents/ctl", nil)
	d := decodeInto[model.Detail](t, b)
	for _, o := range d.Objects {
		if o.Kind == "Secret" || o.Name == "named-secret-canary" {
			t.Errorf("an owned object was derived from status.controller: %+v", o)
		}
	}
	if strings.Contains(d.YAML, "named-secret-canary") {
		t.Error("controller values in the YAML")
	}
	// it is still passed on, opaque, for the collapsed section
	if d.Status == nil || d.Status.Controller["keySecret"] != "named-secret-canary" {
		t.Errorf("status.controller was not passed on: %+v", d.Status)
	}
}

// --- M4: bounded reads ---

func TestListsArePagedAndCapped(t *testing.T) {
	r := newRig(t, func(o *rigOpt) { o.kube = func(k *backend.Kube) { k.PageSize, k.Cap = 2, 5 } })
	for i := 0; i < 7; i++ {
		r.api.seed(fmt.Sprintf("a%d", i), spec("claude-haiku"), map[string]any{})
	}
	_, b := r.do(t, "GET", "/api/v1/agents", nil)
	l := decodeInto[model.AgentList](t, b)
	built := 0
	for _, a := range l.Items {
		if a.Source == "built" {
			built++
		}
	}
	if built != 5 || !l.Truncated || l.Cap != 5 || l.Used != 5 {
		t.Fatalf("built=%d truncated=%v cap=%d used=%d", built, l.Truncated, l.Cap, l.Used)
	}
	pages, cont := 0, 0
	for _, q := range r.api.requests() {
		if strings.HasSuffix(q.Path, "/agentdefinitions") {
			pages++
			if !strings.Contains(q.Query, "limit=2") {
				t.Errorf("no limit in %q", q.Query)
			}
			if strings.Contains(q.Query, "continue=") {
				cont++
			}
		}
	}
	if pages != 3 || cont != 2 {
		t.Errorf("pages=%d continues=%d", pages, cont)
	}
	// below the cap nothing is cut
	r2 := newRig(t, func(o *rigOpt) { o.kube = func(k *backend.Kube) { k.PageSize, k.Cap = 2, 50 } })
	for i := 0; i < 7; i++ {
		r2.api.seed(fmt.Sprintf("a%d", i), spec("claude-haiku"), map[string]any{})
	}
	_, b = r2.do(t, "GET", "/api/v1/agents", nil)
	if l = decodeInto[model.AgentList](t, b); l.Truncated || l.Used != 7 {
		t.Fatalf("%+v", l)
	}
}

func TestAnAPIServerThatIgnoresLimitStillCannotExceedTheCap(t *testing.T) {
	r := newRig(t, func(o *rigOpt) { o.kube = func(k *backend.Kube) { k.PageSize, k.Cap = 100, 3 } })
	r.api.override = func(w http.ResponseWriter, q *http.Request) bool {
		if q.URL.Path == "/apis/openagentix.io/v1alpha1/namespaces/kagent/agentdefinitions" && q.Method == "GET" {
			items := []any{}
			for i := 0; i < 10; i++ {
				items = append(items, map[string]any{"metadata": map[string]any{"name": "n" + strconv.Itoa(i)}, "spec": spec("m")})
			}
			send(w, 200, map[string]any{"items": items, "metadata": map[string]any{"continue": "more"}})
			return true
		}
		return false
	}
	_, b := r.do(t, "GET", "/api/v1/agents", nil)
	l := decodeInto[model.AgentList](t, b)
	built := 0
	for _, a := range l.Items {
		if a.Source == "built" {
			built++
		}
	}
	if built != 3 || !l.Truncated {
		t.Fatalf("built=%d %+v", built, l.Truncated)
	}
}

// --- M7: headers ---

func TestCrossOriginResourcePolicyAndHSTS(t *testing.T) {
	r := newRig(t)
	for _, p := range []string{"/healthz", "/", "/api/v1/session", "/api/v1/nope"} {
		resp, _ := r.do(t, "GET", p, nil)
		if resp.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Errorf("%s: CORP = %q", p, resp.Header.Get("Cross-Origin-Resource-Policy"))
		}
		if resp.Header.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS on a plain-HTTP console by default", p)
		}
	}
	r = newRig(t, withOpts(func(o *server.Options) { o.HSTS = true }))
	for _, p := range []string{"/healthz", "/", "/api/v1/session"} {
		resp, _ := r.do(t, "GET", p, nil)
		if hs := resp.Header.Get("Strict-Transport-Security"); !strings.HasPrefix(hs, "max-age=") {
			t.Errorf("%s: HSTS = %q", p, hs)
		}
	}
	resp, _ := r.do(t, "GET", "/", nil, "Host", "evil.example")
	if resp.StatusCode != 421 || resp.Header.Get("Strict-Transport-Security") == "" || resp.Header.Get("Cross-Origin-Resource-Policy") == "" {
		t.Errorf("the 421 answer lacks the headers: %s %v", resp.Status, resp.Header)
	}
}
