// Package kubetest is a small fake Kubernetes API server for tests. It speaks just enough REST: get, list (with label
// selectors), watch (resume by resourceVersion, forced 410), create (409 when present), server-side apply (modelled as
// a deep merge), merge patch (also on /status), update, and delete (finalizers hold an object with deletionTimestamp).
// It records every request so a test can assert what was sent, and what was never sent.
package kubetest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Object = map[string]any

// Request is one recorded call.
type Request struct {
	Method      string
	Path        string
	Query       string
	ContentType string
	Body        []byte
}

type event struct {
	rv  int
	typ string
	obj Object
}

type Server struct {
	*httptest.Server
	mu     sync.Mutex
	objs   map[string]Object // "plural/ns/name"
	events map[string][]event
	rv     int
	log    []Request
	gone   map[string]string
	// Intercept, when set, may answer a request itself (return handled=true) before the fake does. It runs without the lock.
	Intercept func(r Request) (code int, body any, handled bool)
}

func New() *Server {
	s := &Server{objs: map[string]Object{}, events: map[string][]event{}, gone: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func deepCopy(o Object) Object {
	b, _ := json.Marshal(o)
	var c Object
	_ = json.Unmarshal(b, &c)
	return c
}

func meta(o Object) Object {
	m, _ := o["metadata"].(Object)
	if m == nil {
		m = Object{}
		o["metadata"] = m
	}
	return m
}

// Seed stores an object as if it had been created, and emits an ADDED event.
func (s *Server) Seed(plural string, o Object) Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	o = deepCopy(o)
	m := meta(o)
	ns, _ := m["namespace"].(string)
	name, _ := m["name"].(string)
	s.stampNew(o)
	s.objs[key(plural, ns, name)] = o
	s.emit(plural, ns, "ADDED", o)
	return deepCopy(o)
}

// Get returns a copy of a stored object.
func (s *Server) Get(plural, ns, name string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objs[key(plural, ns, name)]
	if !ok {
		return nil, false
	}
	return deepCopy(o), true
}

// List returns copies of all objects of a plural in a namespace, sorted by name.
func (s *Server) List(plural, ns string) []Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(plural, ns, "")
}

// Modify changes a stored object under the lock (a test playing another actor) and emits MODIFIED.
func (s *Server) Modify(plural, ns, name string, f func(Object)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.objs[key(plural, ns, name)]
	f(o)
	s.bump(o)
	s.emit(plural, ns, "MODIFIED", o)
}

// GoneNextWatch makes the next watch of a collection answer 410 Gone: mode "http" with the HTTP status, "event" with an ERROR event.
func (s *Server) GoneNextWatch(plural, ns, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gone[plural+"/"+ns] = mode
}

// Requests returns a copy of the request log.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.log...)
}

// Matching returns the logged requests of a method whose path contains sub.
func (s *Server) Matching(method, sub string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Method == method && strings.Contains(r.Path, sub) {
			out = append(out, r)
		}
	}
	return out
}

func key(plural, ns, name string) string { return plural + "/" + ns + "/" + name }

func (s *Server) bump(o Object) {
	s.rv++
	meta(o)["resourceVersion"] = strconv.Itoa(s.rv)
}

func (s *Server) stampNew(o Object) {
	m := meta(o)
	if _, ok := m["uid"]; !ok {
		m["uid"] = fmt.Sprintf("uid-%d", s.rv+1)
	}
	if _, ok := m["creationTimestamp"]; !ok {
		m["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
	}
	if _, ok := m["generation"]; !ok {
		m["generation"] = float64(1)
	}
	s.bump(o)
}

func (s *Server) emit(plural, ns, typ string, o Object) {
	rv, _ := strconv.Atoi(meta(o)["resourceVersion"].(string))
	k := plural + "/" + ns
	s.events[k] = append(s.events[k], event{rv: rv, typ: typ, obj: deepCopy(o)})
}

func (s *Server) list(plural, ns, selector string) []Object {
	var out []Object
	prefix := plural + "/" + ns + "/"
	for k, o := range s.objs {
		if strings.HasPrefix(k, prefix) && matches(o, selector) {
			out = append(out, deepCopy(o))
		}
	}
	sort.Slice(out, func(i, j int) bool { return meta(out[i])["name"].(string) < meta(out[j])["name"].(string) })
	return out
}

func matches(o Object, selector string) bool {
	if selector == "" {
		return true
	}
	labels, _ := meta(o)["labels"].(Object)
	for _, part := range strings.Split(selector, ",") {
		k, v, _ := strings.Cut(part, "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}

// parse reads /api/v1/namespaces/<ns>/<plural>[/<name>[/<sub>]] and /apis/<g>/<v>/namespaces/<ns>/<plural>[...].
func parse(path string) (ns, plural, name, sub string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	i := -1
	for j, p := range parts {
		if p == "namespaces" {
			i = j
			break
		}
	}
	if i < 0 || len(parts) < i+3 {
		return
	}
	ns, plural = parts[i+1], parts[i+2]
	if len(parts) > i+3 {
		name = parts[i+3]
	}
	if len(parts) > i+4 {
		sub = parts[i+4]
	}
	return ns, plural, name, sub, true
}

func status(w http.ResponseWriter, code int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(Object{"kind": "Status", "apiVersion": "v1", "status": "Failure", "code": code, "reason": reason, "message": "fake message that a client must not copy"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func merge(dst, patch Object) {
	for k, v := range patch {
		if v == nil {
			delete(dst, k)
			continue
		}
		pm, pok := v.(map[string]any)
		dm, dok := dst[k].(map[string]any)
		if pok && dok {
			merge(dm, pm)
			continue
		}
		dst[k] = v
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, ContentType: r.Header.Get("Content-Type"), Body: body}
	s.mu.Lock()
	s.log = append(s.log, req)
	s.mu.Unlock()
	if s.Intercept != nil {
		if code, b, ok := s.Intercept(req); ok {
			writeJSON(w, code, b)
			return
		}
	}
	ns, plural, name, sub, ok := parse(r.URL.Path)
	if !ok {
		status(w, 404, "NotFound")
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && name == "" && q.Get("watch") == "true":
		s.watch(w, r, plural, ns, q.Get("resourceVersion"))
	case r.Method == http.MethodGet && name == "":
		s.mu.Lock()
		items := s.list(plural, ns, q.Get("labelSelector"))
		rv := strconv.Itoa(s.rv)
		s.mu.Unlock()
		writeJSON(w, 200, Object{"kind": "List", "metadata": Object{"resourceVersion": rv}, "items": items})
	case r.Method == http.MethodGet:
		o, ok := s.Get(plural, ns, name)
		if !ok {
			status(w, 404, "NotFound")
			return
		}
		writeJSON(w, 200, o)
	case r.Method == http.MethodPost && name == "":
		s.create(w, plural, ns, body)
	case r.Method == http.MethodPatch:
		s.patch(w, plural, ns, name, sub, req.ContentType, body)
	case r.Method == http.MethodPut:
		s.update(w, plural, ns, name, body)
	case r.Method == http.MethodDelete:
		s.delete(w, plural, ns, name)
	default:
		status(w, 405, "MethodNotAllowed")
	}
}

func (s *Server) create(w http.ResponseWriter, plural, ns string, body []byte) {
	var o Object
	if json.Unmarshal(body, &o) != nil {
		status(w, 400, "BadRequest")
		return
	}
	name, _ := meta(o)["name"].(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objs[key(plural, ns, name)]; ok {
		status(w, 409, "AlreadyExists")
		return
	}
	meta(o)["namespace"] = ns
	s.stampNew(o)
	s.objs[key(plural, ns, name)] = o
	s.emit(plural, ns, "ADDED", o)
	writeJSON(w, 201, deepCopy(o))
}

func (s *Server) patch(w http.ResponseWriter, plural, ns, name, sub, ct string, body []byte) {
	var p Object
	if json.Unmarshal(body, &p) != nil {
		status(w, 400, "BadRequest")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(plural, ns, name)
	cur, exists := s.objs[k]
	apply := strings.HasPrefix(ct, "application/apply-patch")
	switch {
	case !exists && apply:
		meta(p)["namespace"] = ns
		s.stampNew(p)
		s.objs[k] = p
		s.emit(plural, ns, "ADDED", p)
		writeJSON(w, 201, deepCopy(p))
		return
	case !exists:
		status(w, 404, "NotFound")
		return
	case !apply && !strings.HasPrefix(ct, "application/merge-patch+json"):
		status(w, 415, "UnsupportedMediaType")
		return
	}
	before, _ := json.Marshal(cur["spec"])
	if sub == "status" {
		merge(cur, Object{"status": p["status"]})
	} else {
		merge(cur, p)
		if after, _ := json.Marshal(cur["spec"]); string(before) != string(after) {
			m := meta(cur)
			g, _ := m["generation"].(float64)
			m["generation"] = g + 1
		}
	}
	s.bump(cur)
	s.emit(plural, ns, "MODIFIED", cur)
	writeJSON(w, 200, deepCopy(cur))
}

func (s *Server) update(w http.ResponseWriter, plural, ns, name string, body []byte) {
	var o Object
	if json.Unmarshal(body, &o) != nil {
		status(w, 400, "BadRequest")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(plural, ns, name)
	cur, ok := s.objs[k]
	if !ok {
		status(w, 404, "NotFound")
		return
	}
	if rv, _ := meta(o)["resourceVersion"].(string); rv != "" && rv != meta(cur)["resourceVersion"] {
		status(w, 409, "Conflict")
		return
	}
	// metadata other than finalizers, labels and annotations is kept; status is kept (it has its own subresource)
	keep := deepCopy(cur)
	o["status"] = keep["status"]
	for _, f := range []string{"uid", "creationTimestamp", "generation", "deletionTimestamp"} {
		if v, ok := meta(keep)[f]; ok {
			meta(o)[f] = v
		} else {
			delete(meta(o), f)
		}
	}
	s.bump(o)
	if _, deleting := meta(o)["deletionTimestamp"]; deleting {
		if fin, _ := meta(o)["finalizers"].([]any); len(fin) == 0 {
			delete(s.objs, k)
			s.emit(plural, ns, "DELETED", o)
			writeJSON(w, 200, deepCopy(o))
			return
		}
	}
	s.objs[k] = o
	s.emit(plural, ns, "MODIFIED", o)
	writeJSON(w, 200, deepCopy(o))
}

func (s *Server) delete(w http.ResponseWriter, plural, ns, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(plural, ns, name)
	o, ok := s.objs[k]
	if !ok {
		status(w, 404, "NotFound")
		return
	}
	if fin, _ := meta(o)["finalizers"].([]any); len(fin) > 0 {
		if _, set := meta(o)["deletionTimestamp"]; !set {
			meta(o)["deletionTimestamp"] = time.Now().UTC().Format(time.RFC3339)
			s.bump(o)
			s.emit(plural, ns, "MODIFIED", o)
		}
		writeJSON(w, 200, deepCopy(o))
		return
	}
	delete(s.objs, k)
	s.bump(o)
	s.emit(plural, ns, "DELETED", o)
	writeJSON(w, 200, Object{"kind": "Status", "status": "Success"})
}

func (s *Server) watch(w http.ResponseWriter, r *http.Request, plural, ns, since string) {
	k := plural + "/" + ns
	s.mu.Lock()
	mode := s.gone[k]
	delete(s.gone, k)
	s.mu.Unlock()
	if mode == "http" {
		status(w, 410, "Expired")
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	enc := json.NewEncoder(w)
	if mode == "event" {
		_ = enc.Encode(Object{"type": "ERROR", "object": Object{"kind": "Status", "code": 410, "reason": "Expired"}})
		return
	}
	from, _ := strconv.Atoi(since)
	if fl != nil {
		fl.Flush()
	}
	for {
		s.mu.Lock()
		var batch []event
		for _, e := range s.events[k] {
			if e.rv > from {
				batch = append(batch, e)
			}
		}
		s.mu.Unlock()
		for _, e := range batch {
			if enc.Encode(Object{"type": e.typ, "object": e.obj}) != nil {
				return
			}
			from = e.rv
		}
		if fl != nil {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}
