package a2a

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/turn"
)

type fakeRunner struct {
	mu       sync.Mutex
	gotCtx   string
	gotText  string
	calls    int
	result   turn.Result
}

func (f *fakeRunner) Run(_ context.Context, c, t string) turn.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotCtx, f.gotText = c, t
	r := f.result
	if r.ContextID == "" {
		r.ContextID = c
	}
	return r
}

// The structs below are copied from bridges/telegram/internal/a2a/client.go
// (the real consumer) so we prove its decoder accepts our responses.
type tgPart struct {
	Text string `json:"text,omitempty"`
}
type tgMessage struct {
	MessageID string   `json:"messageId"`
	ContextID string   `json:"contextId,omitempty"`
	Role      string   `json:"role"`
	Parts     []tgPart `json:"parts"`
}
type tgTask struct {
	ContextID string `json:"contextId"`
	Status    struct {
		State   string     `json:"state"`
		Message *tgMessage `json:"message"`
	} `json:"status"`
	Artifacts []struct {
		Parts []tgPart `json:"parts"`
	} `json:"artifacts"`
}
type tgResponse struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		Message *tgMessage `json:"message"`
		Task    *tgTask    `json:"task"`
	} `json:"result"`
}

func post(t *testing.T, h http.Handler, version, body string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/agents/kagent/assistant", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if version != "" {
		req.Header.Set("A2A-Version", version)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, rec.Body.Bytes()
}

func send(ctxID, text string) string {
	m := map[string]any{"messageId": "m1", "role": "ROLE_USER", "parts": []any{map[string]any{"text": text}}}
	if ctxID != "" {
		m["contextId"] = ctxID
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "method": "SendMessage", "params": map[string]any{"message": m}})
	return string(b)
}

func decode(t *testing.T, b []byte) tgResponse {
	t.Helper()
	var r tgResponse
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("decode: %v\n%s", err, b)
	}
	return r
}

func TestCompletedResponse(t *testing.T) {
	fr := &fakeRunner{result: turn.Result{TaskID: "turn-abc", Text: "Paris.",
		Usage:  map[string]any{"inputTokens": 5, "outputTokens": 7, "cachedInputTokens": 0, "totalTokens": 12},
		Claude: map[string]any{"num_turns": 2}}}
	rec, body := post(t, NewHandler(fr), "1.0", send("ctx-9", "capital of France?"))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if fr.gotCtx != "ctx-9" || fr.gotText != "capital of France?" {
		t.Fatalf("runner got %q %q", fr.gotCtx, fr.gotText)
	}
	r := decode(t, body)
	if r.Result.Task == nil || r.Result.Task.Status.State != "TASK_STATE_COMPLETED" || r.Result.Task.ContextID != "ctx-9" {
		t.Fatalf("task: %s", body)
	}
	if len(r.Result.Task.Artifacts) != 1 || r.Result.Task.Artifacts[0].Parts[0].Text != "Paris." {
		t.Fatalf("artifacts: %s", body)
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	task := raw["result"].(map[string]any)["task"].(map[string]any)
	if task["id"] != "turn-abc" || raw["id"] != "1" || raw["jsonrpc"] != "2.0" {
		t.Fatalf("envelope: %s", body)
	}
	md := task["metadata"].(map[string]any)
	usage := md["https://kagent.dev/extensions/usage/v1"].(map[string]any)
	if usage["totalTokens"] != float64(12) || usage["inputTokens"] != float64(5) {
		t.Fatalf("usage: %v", usage)
	}
	if md["openagentix.dev/ax/claude"].(map[string]any)["num_turns"] != float64(2) {
		t.Fatalf("claude metadata: %v", md)
	}
}

func TestFailedResponseIsHTTP200WithStatusMessage(t *testing.T) {
	fr := &fakeRunner{result: turn.Result{TaskID: "turn-x", Failed: true, Text: "agent turn timed out after 170s"}}
	rec, body := post(t, NewHandler(fr), "1.0", send("c", "q"))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	r := decode(t, body)
	task := r.Result.Task
	if task == nil || task.Status.State != "TASK_STATE_FAILED" || len(task.Artifacts) != 0 {
		t.Fatalf("task: %s", body)
	}
	m := task.Status.Message
	if m == nil || m.Role != "ROLE_AGENT" || len(m.Parts) != 1 || m.Parts[0].Text != "agent turn timed out after 170s" {
		t.Fatalf("status.message: %s", body)
	}
	if r.Error != nil {
		t.Fatal("must not be a JSON-RPC error")
	}
}

func TestMintedContextIDPassedThrough(t *testing.T) {
	fr := &fakeRunner{result: turn.Result{ContextID: "minted-1", Text: "hi"}}
	_, body := post(t, NewHandler(fr), "1.0", send("", "hello"))
	if fr.gotCtx != "" {
		t.Fatalf("runner should receive empty contextId so it mints: %q", fr.gotCtx)
	}
	if decode(t, body).Result.Task.ContextID != "minted-1" {
		t.Fatalf("contextId: %s", body)
	}
}

func TestVersionHeader(t *testing.T) {
	fr := &fakeRunner{result: turn.Result{Text: "x"}}
	h := NewHandler(fr)
	if _, body := post(t, h, "", send("c", "q")); decode(t, body).Error != nil {
		t.Fatalf("absent header must be accepted: %s", body)
	}
	rec, body := post(t, h, "0.3", send("c", "q"))
	r := decode(t, body)
	if rec.Code != 200 || r.Error == nil || r.Error.Code != -32009 {
		t.Fatalf("want -32009 for bad version: %d %s", rec.Code, body)
	}
}

func TestJSONRPCErrors(t *testing.T) {
	h := NewHandler(&fakeRunner{})
	cases := []struct {
		name, body string
		code       int
	}{
		{"malformed", `{not json`, -32700},
		{"unknown method", `{"jsonrpc":"2.0","id":"1","method":"Nope","params":{}}`, -32601},
		{"no text", `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"role":"ROLE_USER","parts":[{"data":{}}]}}}`, -32602},
		{"no message", `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{}}`, -32602},
	}
	for _, c := range cases {
		rec, body := post(t, h, "1.0", c.body)
		r := decode(t, body)
		if rec.Code != 200 || r.Error == nil || r.Error.Code != c.code {
			t.Errorf("%s: code=%d body=%s", c.name, rec.Code, body)
		}
	}
}

func TestHealthEndpoints(t *testing.T) {
	h := NewHandler(&fakeRunner{})
	for _, p := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != 200 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

// Our responses must only use keys that kagent's real responses use, at the
// task, status and status.message levels, so consumers written against
// kagent's shape keep working.
func TestShapeSubsetOfKagentSamples(t *testing.T) {
	dir := "../../../telegram/internal/a2a/testdata/"
	keys := func(m map[string]any) map[string]bool {
		out := map[string]bool{}
		for k := range m {
			out[k] = true
		}
		return out
	}
	load := func(name string) map[string]any {
		b, err := os.ReadFile(dir + name)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m["result"].(map[string]any)["task"].(map[string]any)
	}
	okSample := load("a2a-sample-response.json")
	failSample := load("a2a-sample-response-gemini-402-failure.json")

	fr := &fakeRunner{result: turn.Result{TaskID: "t", Text: "a", Usage: map[string]any{"totalTokens": 1}}}
	_, body := post(t, NewHandler(fr), "1.0", send("c", "q"))
	var ours map[string]any
	_ = json.Unmarshal(body, &ours)
	task := ours["result"].(map[string]any)["task"].(map[string]any)
	sub := func(label string, a, b map[string]any) {
		bk := keys(b)
		for k := range a {
			if !bk[k] {
				t.Errorf("%s: key %q not present in kagent sample", label, k)
			}
		}
	}
	sub("completed task", task, okSample)
	sub("completed status", task["status"].(map[string]any), okSample["status"].(map[string]any))
	sub("artifact", task["artifacts"].([]any)[0].(map[string]any), okSample["artifacts"].([]any)[0].(map[string]any))

	fr.result = turn.Result{TaskID: "t", Failed: true, Text: "boom"}
	_, body = post(t, NewHandler(fr), "1.0", send("c", "q"))
	_ = json.Unmarshal(body, &ours)
	task = ours["result"].(map[string]any)["task"].(map[string]any)
	sub("failed task", task, failSample)
	st := task["status"].(map[string]any)
	sub("failed status", st, failSample["status"].(map[string]any))
	sub("failed status.message", st["message"].(map[string]any), failSample["status"].(map[string]any)["message"].(map[string]any))
}
