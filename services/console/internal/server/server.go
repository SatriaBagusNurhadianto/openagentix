// Package server is the console API: a fixed list of routes, each written out here. There is no proxy to the Kubernetes
// API or to any other service. The browser's Authorization header is read by the Authenticator and goes nowhere else;
// the Kubernetes client builds its own requests (package kube), so no browser header can be forwarded.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/openagentix/openagentix/services/console/ext"
	"github.com/openagentix/openagentix/services/console/internal/backend"
	"github.com/openagentix/openagentix/services/console/internal/model"
	"github.com/openagentix/openagentix/services/console/internal/platform"
	"github.com/openagentix/openagentix/services/console/internal/predict"
)

// Limits that protect the console itself.
const (
	MaxBody        = 256 << 10
	MaxHeaderBytes = 16 << 10
)

// CSP is sent with every response. Nothing in the web app needs an inline script or style, an outside host or a frame.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
	"object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// Routes is the whole list, in the order the README documents. The test compares it with what is registered.
var Routes = []string{
	"GET /healthz",
	"GET /api/v1/session",
	"GET /api/v1/platform",
	"GET /api/v1/agents",
	"POST /api/v1/agents",
	"POST /api/v1/agents/validate",
	"GET /api/v1/agents/{name}",
	"PUT /api/v1/agents/{name}",
	"DELETE /api/v1/agents/{name}",
	"/", // every other path and method: the web app for GET and HEAD, a JSON 404 or 405 otherwise
}

// consoleReserved are names the web app uses as routes (/agents/new, /agents/playground).
var consoleReserved = []string{"new", "playground"}

// Options is everything the server needs. Authenticator, Authorizer and AuditSink are the extension points of ADR 0010.
type Options struct {
	Backend        backend.Backend
	Platform       platform.Source
	Authenticator  ext.Authenticator
	Authorizer     ext.Authorizer
	Audit          ext.AuditSink
	Static         fs.FS // the built web app, rooted at its index.html; nil: no web app
	Log            *slog.Logger
	Demo           bool
	Namespace      string
	Context        string
	Version        string
	RequestTimeout time.Duration
}

// Server is the http.Handler.
type Server struct {
	o   Options
	mux *http.ServeMux
	reg []string
}

// New builds the handler.
func New(o Options) *Server {
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 15 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	s := &Server{o: o, mux: http.NewServeMux()}
	s.handle("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	s.handle("GET /api/v1/session", s.api(ext.ActionRead, s.session))
	s.handle("GET /api/v1/platform", s.api(ext.ActionRead, s.platform))
	s.handle("GET /api/v1/agents", s.api(ext.ActionRead, s.list))
	s.handle("POST /api/v1/agents", s.api(ext.ActionCreate, s.create))
	s.handle("POST /api/v1/agents/validate", s.api(ext.ActionValidate, s.validate))
	s.handle("GET /api/v1/agents/{name}", s.api(ext.ActionRead, s.get))
	s.handle("PUT /api/v1/agents/{name}", s.api(ext.ActionUpdate, s.update))
	s.handle("DELETE /api/v1/agents/{name}", s.api(ext.ActionDelete, s.remove))
	s.handle("/", s.fallback)
	return s
}

// Registered returns the patterns in the mux, for the route table test.
func (s *Server) Registered() []string { return append([]string(nil), s.reg...) }

func (s *Server) handle(pattern string, h http.HandlerFunc) {
	s.reg = append(s.reg, pattern)
	s.mux.HandleFunc(pattern, h)
}

// ServeHTTP adds the headers every response carries and a request id, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
		h.Set("Cache-Control", "no-store")
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	rid := hex.EncodeToString(id)
	h.Set("X-Request-Id", rid)
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete:
	default:
		h.Set("Allow", "GET, HEAD, POST, PUT, DELETE")
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "The console API answers GET, HEAD, POST, PUT and DELETE only.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.o.RequestTimeout)
	defer cancel()
	r = r.WithContext(context.WithValue(ctx, ridKey{}, rid))
	sw := &statusWriter{ResponseWriter: w}
	s.mux.ServeHTTP(sw, r)
	s.o.Log.LogAttrs(r.Context(), slog.LevelDebug, "request", slog.String("method", r.Method), slog.String("path", r.URL.Path),
		slog.Int("status", sw.code), slog.String("request_id", rid))
}

type ridKey struct{}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	if w.code == 0 {
		w.code = c
	}
	w.ResponseWriter.WriteHeader(c)
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	return w.ResponseWriter.Write(b)
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var b errBody
	b.Error.Code, b.Error.Message = code, msg
	writeJSON(w, status, b)
}

func (s *Server) backendErr(w http.ResponseWriter, err error) {
	var be *backend.Error
	switch {
	case errors.As(err, &be):
		writeErr(w, be.Status, be.Code, be.Message)
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusGatewayTimeout, "timeout", "The console did not get an answer in time. Try again.")
	case errors.Is(err, context.Canceled):
		writeErr(w, 499, "canceled", "The request was canceled.")
	default:
		s.o.Log.Error("backend", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "The console failed to handle this request. Check the console log for the request id.")
	}
}

// decode reads the JSON body: the right content type, at most MaxBody bytes, no unknown field, nothing after the value.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Send the body as application/json.")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "The request body is larger than 256 KiB.")
		} else {
			writeErr(w, http.StatusBadRequest, "bad_request", "The request body is not valid JSON for this route: "+err.Error()+".")
		}
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "bad_request", "The request body has data after the JSON value.")
		return false
	}
	return true
}

// sameOrigin is the cross-site check for routes that change something. A request from a script of another site carries
// Sec-Fetch-Site: cross-site (or same-site) and an Origin that is not this host. A request with neither header (curl)
// is not a browser, and still needs the bearer token.
func sameOrigin(r *http.Request) bool {
	if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" && sf != "none" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || u.Host != r.Host {
			return false
		}
	}
	return true
}

type call struct {
	ID  ext.Identity
	RID string
	// Name is what the audit line names. A handler whose name comes from the body (create) sets it.
	Name string
}

// api wraps a handler of the API: origin check, authentication, authorisation, and the audit line of a write.
func (s *Server) api(action ext.Action, h func(http.ResponseWriter, *http.Request, *call)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid, _ := r.Context().Value(ridKey{}).(string)
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "cross_origin", "This request came from another origin. The console accepts changes only from its own pages.")
			return
		}
		id, err := s.o.Authenticator.Authenticate(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="openagentix-console"`)
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "Sign in with the console token.")
			return
		}
		res := ext.Resource{Kind: "agentdefinition", Name: r.PathValue("name")}
		sw := &statusWriter{ResponseWriter: w}
		c := &call{ID: id, RID: rid, Name: res.Name}
		audit := func(outcome string) {
			res.Name = c.Name
			if len(res.Name) > 64 {
				res.Name = res.Name[:64]
			}
			if action.Writes() && s.o.Audit != nil {
				s.o.Audit.Record(r.Context(), ext.AuditEvent{Time: time.Now().UTC(), Subject: id.Subject, Action: action, Resource: res,
					Outcome: outcome, Status: sw.code, RequestID: rid})
			}
		}
		if d := s.o.Authorizer.Authorize(r.Context(), id, action, res); !d.Allowed {
			writeErr(sw, http.StatusForbidden, "forbidden", d.Reason)
			audit("denied")
			return
		}
		h(sw, r, c)
		audit(outcomeOf(sw.code))
	}
}

func outcomeOf(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "ok"
	case code == http.StatusConflict:
		return "conflict"
	case code == http.StatusNotFound:
		return "not_found"
	case code == http.StatusBadRequest || code == http.StatusUnprocessableEntity || code == http.StatusRequestEntityTooLarge || code == http.StatusUnsupportedMediaType:
		return "invalid"
	case code == http.StatusForbidden:
		return "denied"
	}
	return "error"
}

// --- handlers ---

type sessionBody struct {
	Subject   string `json:"subject"`
	Role      string `json:"role"`
	CanWrite  bool   `json:"canWrite"`
	Demo      bool   `json:"demo"`
	Version   string `json:"version"`
	Edition   string `json:"edition"`
	Namespace string `json:"namespace"`
	Context   string `json:"context"`
}

func (s *Server) session(w http.ResponseWriter, r *http.Request, c *call) {
	res := ext.Resource{Kind: "agentdefinition"}
	can := s.o.Authorizer.Authorize(r.Context(), c.ID, ext.ActionCreate, res).Allowed
	role := "viewer"
	if can {
		role = "operator"
	}
	demo := s.o.Demo
	if b, err := s.o.Platform.Get(); err == nil && b.Catalog.Demo {
		demo = true
	}
	writeJSON(w, 200, sessionBody{Subject: c.ID.Subject, Role: role, CanWrite: can, Demo: demo, Version: s.o.Version, Edition: "community",
		Namespace: s.o.Namespace, Context: s.o.Context})
}

func (s *Server) both(w http.ResponseWriter) (platform.Both, bool) {
	b, err := s.o.Platform.Get()
	if err != nil {
		s.o.Log.Error("platform files", "err", err)
		writeErr(w, http.StatusServiceUnavailable, "platform_unavailable",
			"Could not read the platform catalog and policy: the console cannot open its mounted files. Check that controlPlane.enabled is true and that the console mounts the control-plane ConfigMaps.")
		return b, false
	}
	return b, true
}

func (s *Server) platform(w http.ResponseWriter, r *http.Request, _ *call) {
	b, ok := s.both(w)
	if !ok {
		return
	}
	writeJSON(w, 200, platform.Summarize(b, s.o.Demo))
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, _ *call) {
	l, err := s.o.Backend.List(r.Context())
	if err != nil {
		s.backendErr(w, err)
		return
	}
	writeJSON(w, 200, l)
}

// pathName returns the {name} of the route when it has the shape of a definition name. Anything else (a path climbing up,
// an encoded slash) is a 404 here and never becomes part of a Kubernetes API path.
func pathName(w http.ResponseWriter, r *http.Request) (string, bool) {
	n := r.PathValue("name")
	if len(n) > model.MaxName || !model.NameRE.MatchString(n) {
		writeErr(w, http.StatusNotFound, "not_found", "There is no agent with that name.")
		return "", false
	}
	return n, true
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, _ *call) {
	name, ok := pathName(w, r)
	if !ok {
		return
	}
	d, err := s.o.Backend.Get(r.Context(), name)
	if err != nil {
		s.backendErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func nameProblem(name string) string {
	switch {
	case name == "":
		return "name is required"
	case len(name) > model.MaxName:
		return "name must be at most 40 characters"
	case !model.NameRE.MatchString(name):
		return "name must be a DNS label: lower case letters, digits and dashes, starting with a letter"
	}
	for _, n := range consoleReserved {
		if name == n {
			return "name \"" + name + "\" is used by a page of the console; pick another"
		}
	}
	return ""
}

func (s *Server) create(w http.ResponseWriter, r *http.Request, c *call) {
	var req model.WriteRequest
	if !decode(w, r, &req) {
		return
	}
	c.Name = req.Name
	if p := nameProblem(req.Name); p != "" {
		writeErr(w, http.StatusUnprocessableEntity, "invalid", "The name is not valid: "+p+".")
		return
	}
	d, err := s.o.Backend.Create(r.Context(), req.Name, req.Spec)
	if err != nil {
		s.backendErr(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/agents/"+url.PathEscape(req.Name))
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) update(w http.ResponseWriter, r *http.Request, _ *call) {
	var req model.WriteRequest
	if !decode(w, r, &req) {
		return
	}
	name, ok := pathName(w, r)
	if !ok {
		return
	}
	if req.Name != "" && req.Name != name {
		writeErr(w, http.StatusUnprocessableEntity, "invalid", "Names cannot change; create a new agent instead.")
		return
	}
	d, err := s.o.Backend.Update(r.Context(), name, req.ResourceVersion, req.Spec)
	if err != nil {
		s.backendErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request, _ *call) {
	name, ok := pathName(w, r)
	if !ok {
		return
	}
	if err := s.o.Backend.Delete(r.Context(), name); err != nil {
		s.backendErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"name": name, "deleting": true})
}

func (s *Server) validate(w http.ResponseWriter, r *http.Request, _ *call) {
	var req model.WriteRequest
	if !decode(w, r, &req) {
		return
	}
	b, ok := s.both(w)
	if !ok {
		return
	}
	res := model.ValidateResult{Predicted: []model.Prediction{}, Prediction: true, Note: model.PredictionNote}
	if req.Name == "" {
		res.Schema = model.SchemaResult{OK: true, Skipped: true, Errors: []string{}}
	} else if p := nameProblem(req.Name); p != "" {
		res.Schema = model.SchemaResult{Errors: []string{p}}
	} else {
		sr, err := s.o.Backend.Schema(r.Context(), req.Name, req.Spec)
		if err != nil {
			s.backendErr(w, err)
			return
		}
		res.Schema = sr
	}
	others := 0
	if l, err := s.o.Backend.List(r.Context()); err == nil {
		others = l.Used
		for _, a := range l.Items {
			if a.Source == "built" && a.Name == req.Name {
				others--
			}
		}
	}
	res.Predicted = append(res.Predicted, predict.Check(predict.Input{Name: req.Name, Spec: req.Spec, Others: others, P: b})...)
	writeJSON(w, 200, res)
}

// --- static files ---

// fallback serves the web app: a file when there is one, index.html for a path the single-page app routes itself, and a
// JSON 404 or 405 for anything under /api/.
func (s *Server) fallback(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") || p == "/api" {
		s.apiMiss(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "This route only answers GET.")
		return
	}
	if s.o.Static == nil {
		http.NotFound(w, r)
		return
	}
	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean != "" {
		if f, err := s.o.Static.Open(clean); err == nil {
			st, serr := f.Stat()
			_ = f.Close()
			if serr == nil && !st.IsDir() {
				s.serveFile(w, r, clean)
				return
			}
		}
		if path.Ext(clean) != "" || strings.HasPrefix(clean, "_app/") {
			http.NotFound(w, r)
			return
		}
	}
	s.serveFile(w, r, "index.html")
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	b, err := fs.ReadFile(s.o.Static, name)
	if err != nil {
		if name == "index.html" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "The web app is not built into this binary. Run services/console/build-web.sh, then rebuild the console.\n")
			return
		}
		http.NotFound(w, r)
		return
	}
	switch {
	case name == "index.html":
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasPrefix(name, "_app/immutable/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(b)))
}

// apiMiss answers an /api/ path that no route took: 405 with Allow when the path exists for another method, else 404.
func (s *Server) apiMiss(w http.ResponseWriter, r *http.Request) {
	var allow []string
	for _, rt := range Routes {
		m, pat, ok := strings.Cut(rt, " ")
		if ok && strings.HasPrefix(pat, "/api/") && matchPath(pat, r.URL.Path) {
			allow = append(allow, m)
		}
	}
	if len(allow) > 0 {
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "This route does not answer "+r.Method+".")
		return
	}
	writeErr(w, http.StatusNotFound, "not_found", "There is no such route.")
}

func matchPath(pattern, p string) bool {
	a, b := strings.Split(pattern, "/"), strings.Split(p, "/")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.HasPrefix(a[i], "{") {
			if b[i] == "" {
				return false
			}
			continue
		}
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// HTTPServer wraps the handler in an http.Server with the limits of this service: headers at most 16 KiB (431 above),
// and read, write and idle timeouts. There is no streaming route, so a write timeout is safe.
func HTTPServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, MaxHeaderBytes: MaxHeaderBytes, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}
}
