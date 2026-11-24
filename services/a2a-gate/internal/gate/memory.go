package gate

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openagentix/openagentix/services/a2a-gate/internal/rules"
)

// Round 3 of ADR 0006: two RPCs of kagent's MemoryService, for callers whose rule says memory: true.
//
//	POST /kagent.runtime.v1.MemoryService/List     (MemoryServiceListRequest{agent_name = 1})
//	POST /kagent.runtime.v1.MemoryService/Delete   (MemoryServiceDeleteRequest{agent_name = 1})
//
// Which user is meant is NOT in the message: kagent takes it from the authenticated principal, which in insecure mode is the
// X-User-Id header (kagent go/core/internal/service/memory/service.go managementScope, go/core/internal/httpserver/auth/authn.go,
// go/core/internal/grpcserver/interceptors.go forwards the x-user-id metadata). The gate sets that header from the caller's rule,
// exactly as for A2A. The message carries only the agent, so the gate parses the one frame, requires exactly the field
// agent_name (anything else, a user field of a later API version included, is refused), and requires the agent to be one the
// rule allows. The bytes it forwards are the bytes it verified. Anything it cannot parse within the bound is refused.
const (
	memoryService = "/kagent.runtime.v1.MemoryService/"
	memoryList    = memoryService + "List"
	memoryDelete  = memoryService + "Delete"
	// A request holds an agent name ("namespace/name", at most 253+1+253 bytes) and a few bytes of framing.
	memoryMaxFrame = 600
	memoryMaxBody  = 5 + memoryMaxFrame
)

func memoryRPC(path string) string {
	switch path {
	case memoryList:
		return "MemoryService/List"
	case memoryDelete:
		return "MemoryService/Delete"
	}
	return ""
}

// serveMemory handles every request whose path is under the MemoryService. The caller is already authenticated.
func (h *handler) serveMemory(w *recorder, r *http.Request, a *auditRec, caller *rules.Caller, claimed []string) {
	refuse := func(code int, reason, text string) {
		a.reason = reason
		deny(w, code, text)
	}
	rpc := memoryRPC(r.URL.Path)
	a.rpc = "unknown"
	if rpc != "" {
		a.rpc = rpc
	}
	// 1. the rule: no memory flag, no memory RPC, whatever else is right
	if !caller.Memory {
		refuse(http.StatusForbidden, "memory", "forbidden")
		return
	}
	// 2. exactly the two RPCs, by POST, in the exact path spelling
	if rpc == "" || r.Method != http.MethodPost || r.URL.EscapedPath() != r.URL.Path {
		refuse(http.StatusForbidden, "path", "forbidden")
		return
	}
	// 3. binary gRPC-Web only: no native gRPC (the gate speaks HTTP/1.1), no base64 text mode, no compression
	if !isGRPCWebBinary(r.Header.Get("Content-Type")) || r.Header.Get("Upgrade") != "" {
		refuse(http.StatusForbidden, "grpc", "forbidden")
		return
	}
	if enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Grpc-Encoding"))); enc != "" && enc != "identity" {
		refuse(http.StatusForbidden, "grpc", "forbidden")
		return
	}
	// 4. the user: from the rule, as for A2A
	id, ok := caller.ResolveUser(claimed)
	if !ok {
		refuse(http.StatusForbidden, "user_id", "forbidden")
		return
	}
	a.userID = id
	// 5. the body: at most one small frame, read within the sniff timeout, and forwarded exactly as verified
	if r.ContentLength > memoryMaxBody {
		refuse(http.StatusRequestEntityTooLarge, "body_too_large", "request too large")
		return
	}
	body, err := h.readSmallBody(w, r, memoryMaxBody)
	switch {
	case errors.Is(err, errTooLarge):
		refuse(http.StatusRequestEntityTooLarge, "body_too_large", "request too large")
		return
	case err != nil:
		w.Header().Set("Connection", "close")
		refuse(http.StatusRequestTimeout, "body_timeout", "request timeout")
		return
	}
	agent, ok := parseMemoryRequest(body)
	if !ok {
		refuse(http.StatusForbidden, "frame", "forbidden")
		return
	}
	ns, name, ok := splitAgent(agent)
	if !ok || !caller.AllowsAgent(ns, name) {
		refuse(http.StatusForbidden, "agent", "forbidden")
		return
	}
	a.agent = ns + "/" + name
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	r.TransferEncoding = nil
	h.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyUser, userVal{id: id, set: true})))
}

var errTooLarge = errors.New("body too large")

// readSmallBody reads the whole body, at most max bytes, within SniffTimeout. The deadline is cleared again afterwards.
func (h *handler) readSmallBody(w http.ResponseWriter, r *http.Request, max int) ([]byte, error) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(h.o.SniffTimeout))
	defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	b, err := io.ReadAll(io.LimitReader(r.Body, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, errTooLarge
	}
	return b, nil
}

func isGRPCWebBinary(ct string) bool {
	ct, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(ct)), ";")
	ct = strings.TrimSpace(ct)
	return ct == "application/grpc-web" || ct == "application/grpc-web+proto"
}

func splitAgent(s string) (ns, name string, ok bool) {
	ns, name, ok = strings.Cut(s, "/")
	if !ok || !k8sName.MatchString(ns) || !k8sName.MatchString(name) {
		return "", "", false
	}
	return ns, name, true
}

// parseMemoryRequest checks one gRPC-Web data frame that holds exactly one protobuf string field, number 1 (agent_name),
// and returns the string. It refuses a trailer or compressed frame, a length that does not match, more than one frame or
// any byte after the frame, a second or an unknown field, a field of another wire type, an empty or invalid UTF-8 name.
func parseMemoryRequest(b []byte) (agent string, ok bool) {
	if len(b) < 5 || b[0] != 0 {
		return "", false
	}
	n := binary.BigEndian.Uint32(b[1:5])
	if n > memoryMaxFrame || int(n) != len(b)-5 {
		return "", false
	}
	msg := b[5:]
	tag, k := binary.Uvarint(msg)
	if k <= 0 || tag != 1<<3|2 { // field 1, length-delimited: nothing else is accepted
		return "", false
	}
	msg = msg[k:]
	l, k := binary.Uvarint(msg)
	if k <= 0 || l == 0 || l != uint64(len(msg)-k) { // the string must fill the rest: no second field, no trailing byte
		return "", false
	}
	s := msg[k:]
	if !utf8.Valid(s) {
		return "", false
	}
	return string(s), true
}
