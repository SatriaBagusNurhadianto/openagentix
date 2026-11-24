package memory

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const canary = "CANARY-MEMORY-TEXT-5521"

// ---- protobuf and gRPC-Web helpers for the fake gate ----

func pbBytes(field int, b []byte) []byte {
	out := []byte{byte(field<<3 | 2)}
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

func pbVarint(field int, v uint64) []byte {
	return binary.AppendUvarint([]byte{byte(field << 3)}, v)
}

func summary(id, content string, access int, expires int64) []byte {
	var m []byte
	m = append(m, pbBytes(1, []byte(id))...)
	m = append(m, pbBytes(2, []byte(content))...)
	m = append(m, pbVarint(3, uint64(access))...)
	m = append(m, pbBytes(4, pbVarint(1, 1700000000))...)
	if expires != 0 {
		m = append(m, pbBytes(5, pbVarint(1, uint64(expires)))...)
	}
	return m
}

func gframe(flag byte, b []byte) []byte {
	h := []byte{flag, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(h[1:], uint32(len(b)))
	return append(h, b...)
}

func okTrailer() []byte { return gframe(0x80, []byte("grpc-status: 0\r\n")) }

type seenReq struct {
	Path   string
	Header http.Header
	Body   []byte
}

type fakeGate struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []seenReq
	// answer builds the response body; status is the HTTP status
	status int
	body   func(path string) []byte
	hdr    map[string]string
}

func newFakeGate(t *testing.T) *fakeGate {
	g := &fakeGate{status: 200}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.seen = append(g.seen, seenReq{r.URL.Path, r.Header.Clone(), b})
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/grpc-web+proto")
		for k, v := range g.hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(g.status)
		if g.body != nil {
			w.Write(g.body(r.URL.Path))
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGate) calls() []seenReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]seenReq(nil), g.seen...)
}

func tokenFile(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newClient(g *fakeGate, t *testing.T) *Client {
	return NewClient(g.srv.URL, "kagent/assistant", g.srv.Client()).WithTokenFile(tokenFile(t, "tok-1\n"))
}

func TestListSendsTheFrameAndDecodesTheItems(t *testing.T) {
	g := newFakeGate(t)
	g.body = func(string) []byte {
		var list []byte
		list = append(list, pbBytes(1, summary("a", "likes tea", 3, 1900000000))...)
		list = append(list, pbBytes(1, summary("b", canary, 0, 0))...)
		return append(gframe(0, list), okTrailer()...)
	}
	items, err := newClient(g, t).List(context.Background(), "telegram:42")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Content != "likes tea" || items[1].Content != canary {
		t.Fatalf("items = %+v", items)
	}
	if items[0].ExpiresAt.Unix() != 1900000000 || !items[1].ExpiresAt.IsZero() {
		t.Errorf("expiry = %v / %v", items[0].ExpiresAt, items[1].ExpiresAt)
	}
	c := g.calls()
	if len(c) != 1 || c[0].Path != "/kagent.runtime.v1.MemoryService/List" {
		t.Fatalf("calls = %+v", c)
	}
	want := gframe(0, pbBytes(1, []byte("kagent/assistant")))
	if string(c[0].Body) != string(want) {
		t.Errorf("request body = %x, want %x", c[0].Body, want)
	}
	h := c[0].Header
	if h.Get("X-User-Id") != "telegram:42" || h.Get("Authorization") != "Bearer tok-1" {
		t.Errorf("identity headers: user %q auth set %v", h.Get("X-User-Id"), h.Get("Authorization") != "")
	}
	if h.Get("Content-Type") != "application/grpc-web+proto" || h.Get("X-Grpc-Web") != "1" {
		t.Errorf("content type %q, x-grpc-web %q", h.Get("Content-Type"), h.Get("X-Grpc-Web"))
	}
}

func TestEmptyList(t *testing.T) {
	g := newFakeGate(t)
	g.body = func(string) []byte { return append(gframe(0, nil), okTrailer()...) }
	items, err := newClient(g, t).List(context.Background(), "telegram:1")
	if err != nil || len(items) != 0 {
		t.Fatalf("items %v err %v", items, err)
	}
	// a response that is only a trailer is an empty list too (an empty message may be left out)
	g.body = func(string) []byte { return okTrailer() }
	if items, err = newClient(g, t).List(context.Background(), "telegram:1"); err != nil || len(items) != 0 {
		t.Fatalf("trailer only: items %v err %v", items, err)
	}
}

func TestDelete(t *testing.T) {
	g := newFakeGate(t)
	g.body = func(string) []byte { return append(gframe(0, pbBytes(1, []byte("deleted"))), okTrailer()...) }
	if err := newClient(g, t).Delete(context.Background(), "telegram:42"); err != nil {
		t.Fatal(err)
	}
	c := g.calls()
	if len(c) != 1 || c[0].Path != "/kagent.runtime.v1.MemoryService/Delete" || c[0].Header.Get("X-User-Id") != "telegram:42" {
		t.Fatalf("calls = %+v", c)
	}
}

// Errors carry numbers (HTTP status, gRPC status) and never text from the response: not the gRPC message, which may hold
// content, not the body of a refusal.
func TestErrorsNeverCarryContent(t *testing.T) {
	g := newFakeGate(t)
	cl := newClient(g, t)
	cases := map[string]func(){
		"grpc status in trailer": func() {
			g.status = 200
			g.body = func(string) []byte { return gframe(0x80, []byte("grpc-status: 7\r\ngrpc-message: "+canary+"\r\n")) }
		},
		"trailers only in headers": func() {
			g.status = 200
			g.hdr = map[string]string{"Grpc-Status": "5", "Grpc-Message": canary}
			g.body = nil
		},
		"http refusal": func() {
			g.status, g.hdr = 403, nil
			g.body = func(string) []byte { return []byte(`{"error":"` + canary + `"}`) }
		},
		"http 500": func() {
			g.status, g.hdr = 500, nil
			g.body = func(string) []byte { return []byte(canary) }
		},
		"garbage body": func() {
			g.status, g.hdr = 200, nil
			g.body = func(string) []byte { return []byte(canary + canary) }
		},
		"truncated frame": func() {
			g.status, g.hdr = 200, nil
			g.body = func(string) []byte { return append(gframe(0, []byte(canary)), 0x80, 0, 0, 0, 9, 'g') }
		},
		"no trailer": func() {
			g.status, g.hdr = 200, nil
			g.body = func(string) []byte { return gframe(0, pbBytes(1, summary("a", canary, 0, 0))) }
		},
		"compressed frame": func() {
			g.status, g.hdr = 200, nil
			g.body = func(string) []byte { return append(gframe(1, []byte(canary)), okTrailer()...) }
		},
		"bad protobuf": func() {
			g.status, g.hdr = 200, nil
			g.body = func(string) []byte { return append(gframe(0, []byte{0x0a, 0x7f, 'x'}), okTrailer()...) }
		},
	}
	for name, setup := range cases {
		setup()
		if _, err := cl.List(context.Background(), "telegram:1"); err == nil {
			t.Errorf("%s: List returned no error", name)
		} else if strings.Contains(err.Error(), canary) {
			t.Errorf("%s: the error carries response content: %v", name, err)
		}
		err := cl.Delete(context.Background(), "telegram:1")
		if err == nil && name != "bad protobuf" { // Delete reads no message
			t.Errorf("%s: Delete returned no error", name)
		} else if err != nil && strings.Contains(err.Error(), canary) {
			t.Errorf("%s: the error carries response content: %v", name, err)
		}
	}
}

func TestErrorCarriesStatusNumbers(t *testing.T) {
	g := newFakeGate(t)
	g.body = func(string) []byte { return gframe(0x80, []byte("grpc-status: 7\r\n")) }
	_, err := newClient(g, t).List(context.Background(), "telegram:1")
	if e, ok := err.(*Error); !ok || e.GRPC != 7 {
		t.Fatalf("err = %#v", err)
	}
	g.status, g.body = 403, nil
	_, err = newClient(g, t).List(context.Background(), "telegram:1")
	if e, ok := err.(*Error); !ok || e.HTTP != 403 {
		t.Fatalf("err = %#v", err)
	}
}

func TestTheTokenFileIsReadOnEveryCall(t *testing.T) {
	g := newFakeGate(t)
	g.body = func(string) []byte { return okTrailer() }
	p := tokenFile(t, "first")
	cl := NewClient(g.srv.URL, "kagent/assistant", g.srv.Client()).WithTokenFile(p)
	cl.List(context.Background(), "telegram:1")
	os.WriteFile(p, []byte("second\n"), 0o600)
	cl.List(context.Background(), "telegram:1")
	c := g.calls()
	if len(c) != 2 || c[0].Header.Get("Authorization") != "Bearer first" || c[1].Header.Get("Authorization") != "Bearer second" {
		t.Fatalf("authorization = %v / %v", c[0].Header.Get("Authorization"), c[1].Header.Get("Authorization"))
	}
}

func TestAMissingOrEmptyTokenFileSendsNothing(t *testing.T) {
	g := newFakeGate(t)
	cl := NewClient(g.srv.URL, "kagent/assistant", g.srv.Client()).WithTokenFile(filepath.Join(t.TempDir(), "absent"))
	if _, err := cl.List(context.Background(), "telegram:1"); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v", err)
	}
	cl = NewClient(g.srv.URL, "kagent/assistant", g.srv.Client()).WithTokenFile(tokenFile(t, " \n"))
	if err := cl.Delete(context.Background(), "telegram:1"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v", err)
	}
	if n := len(g.calls()); n != 0 {
		t.Fatalf("%d requests were sent without a token", n)
	}
}

func TestAnEmptyUserIDIsRefusedBeforeAnyRequest(t *testing.T) {
	g := newFakeGate(t)
	cl := newClient(g, t)
	if _, err := cl.List(context.Background(), ""); err == nil {
		t.Fatal("List without a user id must fail")
	}
	if err := cl.Delete(context.Background(), ""); err == nil {
		t.Fatal("Delete without a user id must fail")
	}
	if n := len(g.calls()); n != 0 {
		t.Fatalf("%d requests were sent", n)
	}
}

func TestHugeResponseIsRefused(t *testing.T) {
	g := newFakeGate(t)
	g.body = func(string) []byte { return []byte(strings.Repeat("x", 5<<20)) }
	if _, err := newClient(g, t).List(context.Background(), "telegram:1"); err == nil {
		t.Fatal("a 5 MiB answer must be refused")
	}
}

func TestTheCallIsBounded(t *testing.T) {
	g := newFakeGate(t)
	block := make(chan struct{})
	g.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block })
	defer close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := newClient(g, t).List(ctx, "telegram:1"); err == nil {
		t.Fatal("expected a timeout")
	}
}

// The List answer of a real kagent 1.0.1 controller (captured live through the A2A gate for a made-up user id "probe:...",
// one memory saved by the agent with ttlDays 30): a MemorySummary with id, content, access_count 1, created_at and expires_at.
const realListData = "0a700a2431646334366638662d613934362d346464372d396132652d616664303338343837396437122a546865207573657227732070726f626520636f64656e616d65206973205445414c2d4f545445522d372e1801220c08aaab96d80610a0aadfe4022a0c08aac5b4d90610f8a2cde302"

func TestDecodesARealKagentAnswer(t *testing.T) {
	data, err := hex.DecodeString(realListData)
	if err != nil {
		t.Fatal(err)
	}
	g := newFakeGate(t)
	g.body = func(string) []byte { return append(gframe(0, data), okTrailer()...) }
	items, err := newClient(g, t).List(context.Background(), "probe:x")
	if err != nil || len(items) != 1 {
		t.Fatalf("items %v err %v", items, err)
	}
	if items[0].Content != "The user's probe codename is TEAL-OTTER-7." {
		t.Errorf("content %q", items[0].Content)
	}
	if items[0].ExpiresAt.UTC().Format("2006-01-02") != "2026-12-24" {
		t.Errorf("expiry %v", items[0].ExpiresAt)
	}
}
