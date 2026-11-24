// Package gate is the A2A gate: a reverse proxy in front of the kagent controller's port 8083 that
//   - authenticates the caller (see package auth),
//   - lets through only the A2A paths for the agents the caller's rule names,
//   - removes every credential and identity header the caller sent and sets X-User-Id itself,
//   - passes SSE through with an immediate flush,
//   - writes one JSON audit line per request, never with message text.
//
// gRPC and gRPC-Web (content type application/grpc*) and every other path are refused, so the controller's
// memory, session and REST APIs are not reachable through it.
package gate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/openagentix/openagentix/services/a2a-gate/internal/auth"
	"github.com/openagentix/openagentix/services/a2a-gate/internal/rules"
)

// Authenticator maps a bearer token to a caller rule (implemented by auth.Authenticator).
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*rules.Caller, error)
}

// Options configure the gate.
type Options struct {
	UpstreamURL string // the controller, e.g. http://kagent-controller.kagent.svc.cluster.local:8083
	Auth        Authenticator
	Audit       *slog.Logger // one record per request, message "audit"
	AuditUserID string       // "hash" (default) or "raw"
	Salt        []byte       // for the hash; random per process by default (set in main)
	SniffBytes  int          // how much of a request body is read to find the contextId (default 64 KiB)
	MaxBody     int64        // largest request body accepted (default 8 MiB)
	Transport   http.RoundTripper
}

const (
	defaultSniff   = 64 << 10
	defaultMaxBody = 8 << 20
)

// request-scoped values for the proxy's Rewrite
type ctxKey int

const keyUser ctxKey = 1

type userVal struct {
	id  string
	set bool
}

type handler struct {
	o     Options
	proxy *httputil.ReverseProxy
}

// New builds the gate handler.
func New(o Options) (http.Handler, error) {
	up, err := url.Parse(o.UpstreamURL)
	if err != nil || (up.Scheme != "http" && up.Scheme != "https") || up.Host == "" {
		return nil, errors.New("upstream must be an http or https URL")
	}
	if o.SniffBytes <= 0 {
		o.SniffBytes = defaultSniff
	}
	if o.MaxBody <= 0 {
		o.MaxBody = defaultMaxBody
	}
	if o.Audit == nil {
		o.Audit = slog.New(slog.DiscardHandler)
	}
	h := &handler{o: o}
	h.proxy = &httputil.ReverseProxy{
		Rewrite:       h.rewrite(up),
		FlushInterval: -1, // SSE: write every chunk through at once
		Transport:     o.Transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if rec, ok := w.(*recorder); ok {
				rec.reason = "upstream"
			}
			deny(w, http.StatusBadGateway, "bad gateway")
		},
	}
	return h, nil
}

// headers that carry or forge an identity, or credentials: never forwarded
var stripHeaders = []string{
	"Authorization", "Proxy-Authorization", "Cookie", "X-User-Id", "X-Agent-Name", "X-Share-Token",
	"X-Kagent-Insecure-Runtime-Identity", "Baggage", "Forwarded", "X-Real-Ip",
}

func (h *handler) rewrite(up *url.URL) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		pr.SetURL(up)
		pr.Out.URL.RawQuery = "" // kagent reads ?user_id= before X-User-Id: no query reaches it
		for _, n := range stripHeaders {
			pr.Out.Header.Del(n)
		}
		for k := range pr.Out.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-forwarded-") {
				delete(pr.Out.Header, k)
			}
		}
		if u, ok := pr.In.Context().Value(keyUser).(userVal); ok && u.set {
			pr.Out.Header.Set("X-User-Id", u.id)
		}
	}
}

// recorder captures the status for the audit line and keeps Flush working.
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
	reason string
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status, r.wrote = http.StatusOK, true
	}
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Flush() {
	if !r.wrote {
		r.status, r.wrote = http.StatusOK, true
	}
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func deny(w http.ResponseWriter, code int, text string) {
	if code == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="a2a-gate"`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, `{"error":"`+text+`"}`+"\n")
}

// fields of the audit line, filled in as the request goes
type auditRec struct {
	caller, agent, userID, contextID, traceID, reason string
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz", "/readyz":
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok\n")
			return
		}
	}
	start := time.Now()
	rec := &recorder{ResponseWriter: w}
	a := &auditRec{traceID: traceID(r.Header.Get("Traceparent"))}
	defer func() {
		st := rec.status
		if st == 0 {
			st = http.StatusOK
		}
		if rec.reason != "" && a.reason == "" {
			a.reason = rec.reason
		}
		h.writeAudit(r, a, st, time.Since(start))
	}()
	h.serve(rec, r, a)
}

func (h *handler) serve(w *recorder, r *http.Request, a *auditRec) {
	refuse := func(code int, reason, text string) {
		a.reason = reason
		deny(w, code, text)
	}
	claimed := r.Header.Values("X-User-Id")
	if len(claimed) > 0 {
		a.userID = claimed[0] // audited (hashed) even when refused: it says what the caller tried
	}

	// 1. who is calling
	token, ok := bearer(r)
	if !ok {
		refuse(http.StatusUnauthorized, "no_token", "unauthorized")
		return
	}
	caller, err := h.o.Auth.Authenticate(r.Context(), token)
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		refuse(http.StatusUnauthorized, "bad_token", "unauthorized")
		return
	case errors.Is(err, auth.ErrUnknownCaller):
		refuse(http.StatusForbidden, "unknown_caller", "forbidden")
		return
	case err != nil:
		refuse(http.StatusServiceUnavailable, "auth_unavailable", "service unavailable")
		return
	}
	a.caller = caller.Name

	// 2. what is it asking for: only A2A paths, never gRPC or gRPC-Web
	if ct := strings.ToLower(r.Header.Get("Content-Type")); strings.HasPrefix(ct, "application/grpc") {
		refuse(http.StatusForbidden, "grpc", "forbidden")
		return
	}
	if r.Header.Get("Upgrade") != "" {
		refuse(http.StatusForbidden, "path", "forbidden")
		return
	}
	ns, name, card, okPath := a2aPath(r)
	if !okPath {
		refuse(http.StatusForbidden, "path", "forbidden")
		return
	}
	a.agent = ns + "/" + name
	if !caller.AllowsAgent(ns, name) {
		refuse(http.StatusForbidden, "agent", "forbidden")
		return
	}

	// 3. which user: the rule decides; the agent card needs no user
	uv := userVal{}
	if card {
		if caller.UserID.Fixed != "" || caller.UserID.Exact != "" {
			uv.id, uv.set = caller.UserID.Fixed+caller.UserID.Exact, true
		}
	} else {
		id, ok := caller.ResolveUser(claimed)
		if !ok {
			refuse(http.StatusForbidden, "user_id", "forbidden")
			return
		}
		uv = userVal{id: id, set: true}
		a.userID = id
	}

	// 4. the body: bounded read for the contextId, the rest streams through untouched
	if r.Method == http.MethodPost {
		if r.ContentLength > h.o.MaxBody {
			refuse(http.StatusRequestEntityTooLarge, "body_too_large", "request too large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, h.o.MaxBody)
		a.contextID = h.sniff(r)
	}
	h.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyUser, uv)))
}

func bearer(r *http.Request) (string, bool) {
	v := r.Header.Values("Authorization")
	if len(v) != 1 {
		return "", false
	}
	scheme, tok, found := strings.Cut(v[0], " ")
	tok = strings.TrimSpace(tok)
	if !found || !strings.EqualFold(scheme, "Bearer") || tok == "" {
		return "", false
	}
	return tok, true
}

var k8sName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// a2aPath accepts the A2A routes of one agent and nothing else:
//
//	POST /agents/<ns>/<name>                (JSON-RPC; a trailing slash is fine)
//	POST /agents/<ns>/<name>/[v1/]message:send|message:stream
//	GET  /agents/<ns>/<name>/.well-known/agent-card.json|agent.json
func a2aPath(r *http.Request) (ns, name string, card, ok bool) {
	p := r.URL.EscapedPath()
	if p != r.URL.Path || strings.Contains(p, "//") || strings.Contains(p, "%") || strings.Contains(p, "\\") {
		return
	}
	seg := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(seg) < 3 || seg[0] != "agents" || !k8sName.MatchString(seg[1]) || !k8sName.MatchString(seg[2]) {
		return
	}
	rest := strings.Join(seg[3:], "/")
	switch r.Method {
	case http.MethodPost:
		switch rest {
		case "", "message:send", "message:stream", "v1/message:send", "v1/message:stream":
			return seg[1], seg[2], false, true
		}
	case http.MethodGet:
		switch rest {
		case ".well-known/agent-card.json", ".well-known/agent.json":
			return seg[1], seg[2], true, true
		}
	}
	return
}

var contextIDRe = regexp.MustCompile(`"contextId"\s*:\s*"([^"\\]{0,1000})"`)
var contextIDOK = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// sniff reads at most SniffBytes of the request body to find the contextId, then puts the bytes back in
// front of the rest of the body. Only the id is returned; the text is never kept or logged.
func (h *handler) sniff(r *http.Request) string {
	buf := make([]byte, h.o.SniffBytes)
	n, _ := io.ReadFull(r.Body, buf)
	buf = buf[:n]
	orig := r.Body
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), orig), orig}
	m := contextIDRe.FindSubmatch(buf)
	if m == nil {
		return ""
	}
	if !contextIDOK.Match(m[1]) {
		return "invalid"
	}
	return string(m[1])
}

func traceID(tp string) string {
	p := strings.Split(strings.TrimSpace(tp), "-")
	if len(p) != 4 || len(p[1]) != 32 || p[1] == strings.Repeat("0", 32) {
		return ""
	}
	for _, c := range p[1] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return p[1]
}

func (h *handler) hashUser(id string) string {
	if id == "" {
		return ""
	}
	if h.o.AuditUserID == "raw" {
		return id
	}
	s := sha256.Sum256(append(append([]byte{}, h.o.Salt...), id...))
	return "h:" + hex.EncodeToString(s[:])[:8]
}

func (h *handler) writeAudit(r *http.Request, a *auditRec, status int, d time.Duration) {
	attrs := []any{
		"caller", a.caller, "agent", a.agent, "user_id", h.hashUser(a.userID), "context_id", a.contextID,
		"trace_id", a.traceID, "status", status, "duration_ms", d.Milliseconds(), "method", r.Method,
	}
	if a.reason != "" {
		attrs = append(attrs, "reason", a.reason)
	}
	h.o.Audit.Info("audit", attrs...)
}
