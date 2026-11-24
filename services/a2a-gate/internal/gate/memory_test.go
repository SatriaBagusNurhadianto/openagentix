package gate

import (
	"bufio"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	listPath   = "/kagent.runtime.v1.MemoryService/List"
	deletePath = "/kagent.runtime.v1.MemoryService/Delete"
	grpcWeb    = "application/grpc-web+proto"
)

// pbString is the protobuf encoding of one string field.
func pbString(field int, s string) []byte {
	b := []byte{byte(field<<3 | 2)}
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// frame wraps a message in one gRPC-Web data frame (flag 0, 4 byte big endian length).
func frame(msg []byte) string {
	h := make([]byte, 5)
	binary.BigEndian.PutUint32(h[1:], uint32(len(msg)))
	return string(h) + string(msg)
}

func memReq(agent string) string { return frame(pbString(1, agent)) }

func memHdr(user string) map[string]string {
	h := map[string]string{"Content-Type": grpcWeb, "X-Grpc-Web": "1"}
	if user != "" {
		h["X-User-Id"] = user
	}
	return h
}

// A caller whose rule has memory: true may call List and Delete for its own agent and user ids, and the gate sets the identity.
func TestMemoryRPCPassesWithTheMemoryFlag(t *testing.T) {
	for _, p := range []string{listPath, deletePath} {
		r := newRig(t, nil)
		resp, _ := r.do("POST", p, tokBridgeMem, memHdr("telegram:42"), memReq("kagent/assistant"))
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status %d, want 200", p, resp.StatusCode)
		}
		calls := r.upstreamCalls()
		if len(calls) != 1 {
			t.Fatalf("%s: upstream calls = %d", p, len(calls))
		}
		c := calls[0]
		if c.URL != p || c.Method != "POST" {
			t.Errorf("upstream saw %s %s", c.Method, c.URL)
		}
		if got := c.Header.Get("X-User-Id"); got != "telegram:42" {
			t.Errorf("upstream X-User-Id = %q", got)
		}
		if c.Header.Get("Authorization") != "" {
			t.Error("the caller's Authorization reached the controller")
		}
		if string(c.Body) != memReq("kagent/assistant") {
			t.Error("the body that reached the controller is not the verified frame")
		}
		if ct := c.Header.Get("Content-Type"); ct != grpcWeb {
			t.Errorf("upstream content type %q", ct)
		}
	}
}

// Without memory: true on the rule, every memory RPC stays refused, whatever the body.
func TestMemoryRPCNeedsTheFlag(t *testing.T) {
	r := newRig(t, nil)
	for _, tok := range []string{tokBridge, tokSchedule, keyKourier} {
		for _, p := range []string{listPath, deletePath} {
			resp, _ := r.do("POST", p, tok, memHdr("telegram:42"), memReq("kagent/assistant"))
			if resp.StatusCode != 403 {
				t.Errorf("%s as %.8s: status %d, want 403", p, tok, resp.StatusCode)
			}
		}
	}
	if n := len(r.upstreamCalls()); n != 0 {
		t.Fatalf("refused requests reached the upstream %d times", n)
	}
	r.waitLines(6)
	for _, l := range r.auditLines() {
		if l["reason"] != "memory" {
			t.Errorf("reason = %v, want memory", l["reason"])
		}
	}
}

// The user id comes from the rule, not from the message: another user's id in the header is refused, and the message has no
// user field to put one in. A message that tries is refused.
func TestMemoryRPCUserIDRules(t *testing.T) {
	r := newRig(t, nil)
	cases := map[string]string{
		"id outside the prefix": "schedule:x",
		"other prefix":          "kourier:b",
		"no id":                 "",
		"only the prefix":       "telegram:",
	}
	for name, id := range cases {
		resp, _ := r.do("POST", listPath, tokBridgeMem, memHdr(id), memReq("kagent/assistant"))
		if resp.StatusCode != 403 {
			t.Errorf("%s: status %d, want 403", name, resp.StatusCode)
		}
	}
	// two X-User-Id values are ambiguous
	req, _ := http.NewRequest("POST", r.srv.URL+listPath, strings.NewReader(memReq("kagent/assistant")))
	req.Header.Set("Authorization", "Bearer "+tokBridgeMem)
	req.Header.Set("Content-Type", grpcWeb)
	req.Header.Add("X-User-Id", "telegram:1")
	req.Header.Add("X-User-Id", "telegram:2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("two ids: status %d, want 403", resp.StatusCode)
	}
	// the query string is dropped before the controller, which reads ?user_id= first
	resp, _ = r.do("POST", listPath+"?user_id=telegram:999", tokBridgeMem, memHdr("telegram:42"), memReq("kagent/assistant"))
	if resp.StatusCode != 200 {
		t.Fatalf("query: status %d", resp.StatusCode)
	}
	calls := r.upstreamCalls()
	if len(calls) != 1 || calls[0].URL != listPath || calls[0].Header.Get("X-User-Id") != "telegram:42" {
		t.Errorf("query reached the controller: %+v", calls)
	}
}

// Whatever else the message carries is refused: an agent the rule does not allow, a second or an unknown field (a user
// field of some later version of the API, say), a repeated agent field, bad UTF-8.
func TestMemoryRPCMessageIsVerified(t *testing.T) {
	r := newRig(t, nil)
	bad := map[string]string{
		"other agent":        memReq("kagent/other"),
		"empty message":      frame(nil),
		"empty agent":        frame(pbString(1, "")),
		"agent twice":        frame(append(pbString(1, "kagent/assistant"), pbString(1, "kagent/assistant")...)),
		"extra string field": frame(append(pbString(1, "kagent/assistant"), pbString(2, "telegram:999")...)),
		"user field first":   frame(append(pbString(2, "telegram:999"), pbString(1, "kagent/assistant")...)),
		"varint field":       frame(append(pbString(1, "kagent/assistant"), 0x18, 0x01)),
		"wrong wire type":    frame([]byte{0x08, 0x01}),
		"bad utf8":           frame(pbString(1, "kagent/\xff\xfe")),
		"agent not ns/name":  memReq("assistant"),
		"agent with user":    memReq("kagent/assistant telegram:999"),
		"length too long":    frame([]byte{0x0a, 0x7f, 'k'}),
		"truncated varint":   frame([]byte{0x0a, 0x80}),
	}
	for name, body := range bad {
		resp, _ := r.do("POST", listPath, tokBridgeMem, memHdr("telegram:42"), body)
		if resp.StatusCode != 403 {
			t.Errorf("%s: status %d, want 403", name, resp.StatusCode)
		}
	}
	if n := len(r.upstreamCalls()); n != 0 {
		t.Fatalf("refused messages reached the controller %d times", n)
	}
}

// Frames that cannot be parsed within the bound fail closed.
func TestMemoryRPCFramesFailClosed(t *testing.T) {
	r := newRig(t, nil)
	good := memReq("kagent/assistant")
	trailer := string([]byte{0x80, 0, 0, 0, 0})
	cmpFlag := []byte(good)
	cmpFlag[0] = 1
	long := frame(pbString(1, "kagent/"+strings.Repeat("a", 3000)))
	cases := map[string]struct {
		body  string
		codes []int
	}{
		"empty body":          {"", []int{403}},
		"short header":        {good[:3], []int{403}},
		"header only":         {good[:5], []int{403}},
		"truncated message":   {good[:len(good)-1], []int{403}},
		"trailing bytes":      {good + "x", []int{403}},
		"second frame":        {good + good, []int{403}},
		"trailer frame":       {good + trailer, []int{403}},
		"trailer as first":    {trailer, []int{403}},
		"compressed flag":     {string(cmpFlag), []int{403}},
		"length lies (short)": {good[:1] + string([]byte{0, 0, 0, 1}) + good[5:], []int{403}},
		"length 4 GiB":        {good[:1] + string([]byte{0xff, 0xff, 0xff, 0xff}) + good[5:], []int{403, 413}},
		"over the bound":      {long, []int{403, 413}},
	}
	for name, tc := range cases {
		resp, _ := r.do("POST", listPath, tokBridgeMem, memHdr("telegram:42"), tc.body)
		okc := false
		for _, c := range tc.codes {
			okc = okc || resp.StatusCode == c
		}
		if !okc {
			t.Errorf("%s: status %d, want one of %v", name, resp.StatusCode, tc.codes)
		}
	}
	if n := len(r.upstreamCalls()); n != 0 {
		t.Fatalf("refused frames reached the controller %d times", n)
	}
}

// Only the two RPCs, only POST, only binary gRPC-Web: everything else under the memory service, and every other
// service, stays refused for a caller that has memory: true.
func TestMemoryFlagOpensOnlyListAndDelete(t *testing.T) {
	r := newRig(t, nil)
	body := memReq("kagent/assistant")
	cases := []struct{ method, path, ctype string }{
		{"POST", "/kagent.runtime.v1.MemoryService/Search", grpcWeb},
		{"POST", "/kagent.runtime.v1.MemoryService/AddSession", grpcWeb},
		{"POST", "/kagent.runtime.v1.MemoryService/AddSessionBatch", grpcWeb},
		{"POST", "/kagent.runtime.v1.MemoryService/", grpcWeb},
		{"POST", "/kagent.runtime.v1.MemoryService", grpcWeb},
		{"POST", "/kagent.runtime.v1.MemoryService/List/", grpcWeb},
		{"POST", "/kagent.runtime.v1.MemoryService/list", grpcWeb},
		{"POST", "/kagent.runtime.v1.MemoryService/List%2f", grpcWeb},
		{"POST", "//kagent.runtime.v1.MemoryService/List", grpcWeb},
		{"POST", "/api/kagent.runtime.v1.MemoryService/List", grpcWeb},
		{"POST", "/kagent.api.v1.SessionService/List", grpcWeb},
		{"POST", "/kagent.api.v1.AgentService/List", grpcWeb},
		{"POST", "/kagent.runtime.v1.TaskStoreService/Get", grpcWeb},
		{"GET", listPath, grpcWeb},
		{"PUT", listPath, grpcWeb},
		{"DELETE", deletePath, grpcWeb},
		{"POST", listPath, "application/grpc"},
		{"POST", listPath, "application/grpc+proto"},
		{"POST", listPath, "application/grpc-web-text"},
		{"POST", listPath, "application/grpc-web-text+proto"},
		{"POST", listPath, "application/json"},
		{"POST", listPath, ""},
	}
	for _, c := range cases {
		resp, _ := r.do(c.method, c.path, tokBridgeMem, map[string]string{"Content-Type": c.ctype, "X-User-Id": "telegram:42"}, body)
		if resp.StatusCode != 403 && resp.StatusCode != 404 {
			t.Errorf("%s %s (%s): status %d, want 403", c.method, c.path, c.ctype, resp.StatusCode)
		}
	}
	// compression and upgrade headers are refused rather than interpreted
	h := memHdr("telegram:42")
	h["Grpc-Encoding"] = "gzip"
	if resp, _ := r.do("POST", listPath, tokBridgeMem, h, body); resp.StatusCode != 403 {
		t.Errorf("grpc-encoding gzip: status %d", resp.StatusCode)
	}
	if n := len(r.upstreamCalls()); n != 0 {
		t.Fatalf("refused requests reached the controller %d times", n)
	}
}

// A caller with memory: true still gets 401 without a token, and the memory RPC does not widen the A2A rule.
func TestMemoryRPCNeedsAToken(t *testing.T) {
	r := newRig(t, nil)
	if resp, _ := r.do("POST", listPath, "", memHdr("telegram:42"), memReq("kagent/assistant")); resp.StatusCode != 401 {
		t.Errorf("no token: status %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", listPath, tokExpired, memHdr("telegram:42"), memReq("kagent/assistant")); resp.StatusCode != 401 {
		t.Errorf("expired: status %d", resp.StatusCode)
	}
	// the method list of the rule does not apply to memory (SendMessage only here) and A2A is unchanged
	if resp, _ := r.do("POST", agentPath, tokBridgeMem, map[string]string{"X-User-Id": "telegram:42"}, msg("c1")); resp.StatusCode != 200 {
		t.Errorf("A2A through a memory caller: status %d", resp.StatusCode)
	}
}

// A slow or oversized body must not hold the gate: the body is bounded and read within the sniff timeout.
func TestMemoryRPCBodyIsBounded(t *testing.T) {
	r := newRig(t, nil)
	big := frame(pbString(1, "kagent/assistant")) + strings.Repeat("x", 1<<16)
	resp, _ := r.do("POST", listPath, tokBridgeMem, memHdr("telegram:42"), big)
	if resp.StatusCode != 403 && resp.StatusCode != 413 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if n := len(r.upstreamCalls()); n != 0 {
		t.Fatalf("reached the controller %d times", n)
	}
}

// One audit line per memory call with the rpc field, the agent and the hashed user id, and never any content.
func TestMemoryRPCAuditLine(t *testing.T) {
	r := newRig(t, nil)
	r.do("POST", listPath, tokBridgeMem, memHdr("telegram:42"), memReq("kagent/assistant"))
	r.do("POST", deletePath, tokBridgeMem, memHdr("telegram:42"), memReq("kagent/assistant"))
	r.do("POST", listPath, tokBridgeMem, memHdr("schedule:x"), memReq("kagent/assistant"))
	r.do("POST", listPath, tokBridgeMem, memHdr("telegram:42"), frame(pbString(1, "kagent/"+canary)))
	r.waitLines(4)
	ls := r.auditLines()
	if ls[0]["rpc"] != "MemoryService/List" || ls[0]["caller"] != "bridge-mem" || ls[0]["agent"] != "kagent/assistant" || ls[0]["status"] != float64(200) {
		t.Errorf("list line: %v", ls[0])
	}
	if ls[1]["rpc"] != "MemoryService/Delete" {
		t.Errorf("delete line: %v", ls[1])
	}
	if u, _ := ls[0]["user_id"].(string); !strings.HasPrefix(u, "h:") || strings.Contains(u, "42") {
		t.Errorf("user id in the audit line is not a hash: %v", ls[0]["user_id"])
	}
	if ls[2]["reason"] != "user_id" || ls[2]["rpc"] != "MemoryService/List" {
		t.Errorf("refused user line: %v", ls[2])
	}
	if ls[3]["reason"] != "agent" {
		t.Errorf("refused agent line: %v", ls[3])
	}
	if strings.Contains(r.audit.String(), canary) || strings.Contains(r.audit.String(), "telegram:42") {
		t.Error("the audit line carries content or a raw user id")
	}
}

func TestMemoryRPCSlowBodyIsCut(t *testing.T) {
	r := newRig(t, func(o *Options) { o.SniffTimeout = 200 * time.Millisecond })
	c := rawConn(t, r.srv.URL)
	io.WriteString(c, "POST "+listPath+" HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer "+tokBridgeMem+"\r\nContent-Type: "+grpcWeb+"\r\nX-User-Id: telegram:1\r\nContent-Length: 30\r\n\r\n\x00\x00\x00")
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.Contains(line, "408") {
		t.Fatalf("status line %q err %v, want 408", line, err)
	}
	if len(r.upstreamCalls()) != 0 {
		t.Fatal("a half-sent body reached the controller")
	}
}
