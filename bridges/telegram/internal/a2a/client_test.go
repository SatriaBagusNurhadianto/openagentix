package a2a

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type captured struct {
	header http.Header
	body   map[string]any
}

func fakeAgent(t *testing.T, status int, response string, cap *captured) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cap != nil {
			cap.header = r.Header.Clone()
			_ = json.NewDecoder(r.Body).Decode(&cap.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
}

func TestSend_DirectMessageResult(t *testing.T) {
	var cap captured
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"message":{"messageId":"m2","contextId":"ctx-1","role":"ROLE_AGENT","parts":[{"text":"hello "},{"text":"there"}]}}}`, &cap)
	defer srv.Close()

	c := NewClient(srv.URL, "", srv.Client())
	reply, ctxID, err := c.Send(context.Background(), "", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "hello \nthere" {
		t.Fatalf("reply = %q", reply)
	}
	if ctxID != "ctx-1" {
		t.Fatalf("ctxID = %q", ctxID)
	}
}

func msgOf(t *testing.T, cap captured) map[string]any {
	t.Helper()
	params := cap.body["params"].(map[string]any)
	return params["message"].(map[string]any)
}

func TestSend_RequestShape(t *testing.T) {
	var cap captured
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"message":{"messageId":"x","role":"ROLE_AGENT","parts":[{"text":"ok"}]}}}`, &cap)
	defer srv.Close()
	c := NewClient(srv.URL, "", srv.Client())

	if _, _, err := c.Send(context.Background(), "", "hi"); err != nil {
		t.Fatal(err)
	}
	if cap.header.Get("A2A-Version") != "1.0" {
		t.Fatalf("A2A-Version = %q", cap.header.Get("A2A-Version"))
	}
	if cap.header.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type = %q", cap.header.Get("Content-Type"))
	}
	if cap.header.Get("Authorization") != "" {
		t.Fatalf("unexpected auth header")
	}
	if cap.body["jsonrpc"] != "2.0" || cap.body["method"] != "SendMessage" {
		t.Fatalf("body = %v", cap.body)
	}
	m := msgOf(t, cap)
	if m["role"] != "ROLE_USER" {
		t.Fatalf("role = %v", m["role"])
	}
	if _, ok := m["contextId"]; ok {
		t.Fatalf("contextId must be absent on first turn")
	}
	parts := m["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "hi" {
		t.Fatalf("parts = %v", parts)
	}
	if _, ok := parts[0].(map[string]any)["kind"]; ok {
		t.Fatalf("parts must not carry kind")
	}
	first := m["messageId"].(string)

	if _, _, err := c.Send(context.Background(), "ctx-9", "again"); err != nil {
		t.Fatal(err)
	}
	m2 := msgOf(t, cap)
	if m2["contextId"] != "ctx-9" {
		t.Fatalf("contextId = %v", m2["contextId"])
	}
	if m2["messageId"] == first || m2["messageId"] == "" {
		t.Fatalf("messageId not unique: %v vs %v", first, m2["messageId"])
	}
}

func TestSend_BearerToken(t *testing.T) {
	var cap captured
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"message":{"role":"ROLE_AGENT","parts":[{"text":"ok"}]}}}`, &cap)
	defer srv.Close()
	c := NewClient(srv.URL, "s3cret", srv.Client())
	if _, _, err := c.Send(context.Background(), "", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := cap.header.Get("Authorization"); got != "Bearer s3cret" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestSend_TaskResult_ArtifactsAndStatusMessage(t *testing.T) {
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t1","contextId":"ctx-7",
	"status":{"state":"TASK_STATE_COMPLETED","message":{"messageId":"s","role":"ROLE_AGENT","parts":[{"text":"status says"}]}},
	"artifacts":[{"artifactId":"a1","parts":[{"text":"art one"},{"text":"art two"}]},{"artifactId":"a2","parts":[{"text":"art three"}]}]}}}`, nil)
	defer srv.Close()
	c := NewClient(srv.URL, "", srv.Client())
	reply, ctxID, err := c.Send(context.Background(), "", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if ctxID != "ctx-7" {
		t.Fatalf("ctxID = %q", ctxID)
	}
	want := "art one\nart two\nart three\nstatus says"
	if reply != want {
		t.Fatalf("reply = %q, want %q", reply, want)
	}
}

func TestSend_TaskResult_StatusMessageOnly(t *testing.T) {
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t1","contextId":"c","status":{"state":"TASK_STATE_INPUT_REQUIRED","message":{"role":"ROLE_AGENT","parts":[{"text":"need more info"}]}}}}}`, nil)
	defer srv.Close()
	reply, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi")
	if err != nil || reply != "need more info" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
}

func TestSend_EmptyReplyIsError(t *testing.T) {
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t1","contextId":"c","status":{"state":"TASK_STATE_WORKING"}}}}`, nil)
	defer srv.Close()
	_, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi")
	if err == nil {
		t.Fatal("expected error for reply with no text")
	}
}

func TestSend_JSONRPCError(t *testing.T) {
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","error":{"code":-32001,"message":"Task not found"}}`, nil)
	defer srv.Close()
	_, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi")
	if err == nil || !strings.Contains(err.Error(), "-32001") || !strings.Contains(err.Error(), "Task not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_Non2xx(t *testing.T) {
	srv := fakeAgent(t, 503, `upstream down`, nil)
	defer srv.Close()
	_, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi")
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_MalformedJSON(t *testing.T) {
	srv := fakeAgent(t, 200, `not json`, nil)
	defer srv.Close()
	if _, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSend_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // lets the server notice the client disconnect
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := NewClient(srv.URL, "", srv.Client()).Send(ctx, "", "hi"); err == nil {
		t.Fatal("expected timeout error")
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSend_RealFailedTaskIsError(t *testing.T) {
	srv := fakeAgent(t, 200, fixture(t, "a2a-sample-response-gemini-402-failure.json"), nil)
	defer srv.Close()
	reply, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi")
	if err == nil {
		t.Fatalf("expected error for TASK_STATE_FAILED, got reply %q", reply)
	}
	if reply != "" {
		t.Fatalf("reply must be empty on failure, got %q", reply)
	}
	if !strings.Contains(err.Error(), "TASK_STATE_FAILED") || !strings.Contains(err.Error(), "402") {
		t.Fatalf("error should carry state and provider detail for logging: %v", err)
	}
}

func TestSend_CanceledAndRejectedTasksAreErrors(t *testing.T) {
	for _, st := range []string{"TASK_STATE_CANCELED", "TASK_STATE_REJECTED"} {
		srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t","contextId":"c","status":{"state":"`+st+`","message":{"role":"ROLE_AGENT","parts":[{"text":"internal detail"}]}}}}}`, nil)
		reply, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi")
		srv.Close()
		if err == nil || reply != "" || !strings.Contains(err.Error(), st) {
			t.Fatalf("%s: reply=%q err=%v", st, reply, err)
		}
	}
}

func TestSend_RealSuccessSamples(t *testing.T) {
	for name, want := range map[string]string{
		"a2a-sample-response.json":          "MOCK-LLM reply (2 messages seen). 2+2 is 4.",
		"a2a-sample-response-followup.json": "MOCK-LLM reply (4 messages seen). 2+2 is 4.",
	} {
		srv := fakeAgent(t, 200, fixture(t, name), nil)
		reply, ctxID, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi")
		srv.Close()
		if err != nil || reply != want || ctxID != "01a20200-256c-7d2a-bf1d-43900dfbbbb2" {
			t.Fatalf("%s: reply=%q ctx=%q err=%v", name, reply, ctxID, err)
		}
	}
}

func TestSend_InputRequiredRelaysQuestion_WorkingWithoutTextErrors(t *testing.T) {
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t","contextId":"c1","status":{"state":"TASK_STATE_INPUT_REQUIRED","message":{"role":"ROLE_AGENT","parts":[{"text":"Which city?"}]}}}}}`, nil)
	reply, ctxID, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "weather")
	srv.Close()
	if err != nil || reply != "Which city?" || ctxID != "c1" {
		t.Fatalf("reply=%q ctx=%q err=%v", reply, ctxID, err)
	}
	srv = fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}}`, nil)
	defer srv.Close()
	if _, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "x"); err == nil {
		t.Fatal("WORKING without text must be an error")
	}
}

func TestSend_NonTerminalTasksAreErrorsEvenWithText(t *testing.T) {
	for _, st := range []string{"TASK_STATE_WORKING", "TASK_STATE_SUBMITTED", "TASK_STATE_UNSPECIFIED", ""} {
		srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t","contextId":"c","status":{"state":"`+st+`","message":{"role":"ROLE_AGENT","parts":[{"text":"still thinking"}]}},"artifacts":[{"parts":[{"text":"partial"}]}]}}}`, nil)
		c := NewClient(srv.URL, "", srv.Client())
		reply, ctxID, err := c.Send(context.Background(), "", "hi")
		srv.Close()
		if err == nil || reply != "" {
			t.Fatalf("state %q: reply=%q err=%v, want error and no reply", st, reply, err)
		}
		if ctxID != "c" {
			t.Fatalf("state %q: context id should still be returned, got %q", st, ctxID)
		}
	}
}

func TestSend_AuthRequiredRelaysAgentMessage(t *testing.T) {
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t","contextId":"c","status":{"state":"TASK_STATE_AUTH_REQUIRED","message":{"role":"ROLE_AGENT","parts":[{"text":"Please sign in"}]}}}}}`, nil)
	defer srv.Close()
	reply, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi")
	if err != nil || reply != "Please sign in" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
}

func TestSend_OversizedBodyIsError(t *testing.T) {
	big := `{"jsonrpc":"2.0","id":"1","result":{"message":{"role":"ROLE_AGENT","parts":[{"text":"` + strings.Repeat("a", 5<<20) + `"}]}}}`
	srv := fakeAgent(t, 200, big, nil)
	defer srv.Close()
	reply, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi")
	if err == nil || reply != "" || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("reply len=%d err=%v", len(reply), err)
	}
}

func TestSend_UserIDHeaderFromContext(t *testing.T) {
	var cap captured
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"message":{"role":"ROLE_AGENT","parts":[{"text":"ok"}]}}}`, &cap)
	defer srv.Close()
	ctx := WithUserID(context.Background(), "telegram:42")
	if _, _, err := NewClient(srv.URL, "", srv.Client()).Send(ctx, "", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := cap.header.Get("X-User-Id"); got != "telegram:42" {
		t.Fatalf("X-User-Id = %q, want telegram:42", got)
	}
}

func TestSend_NoUserIDHeaderWithoutContextValue(t *testing.T) {
	var cap captured
	srv := fakeAgent(t, 200, `{"jsonrpc":"2.0","id":"1","result":{"message":{"role":"ROLE_AGENT","parts":[{"text":"ok"}]}}}`, &cap)
	defer srv.Close()
	if _, _, err := NewClient(srv.URL, "", srv.Client()).Send(context.Background(), "", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, present := cap.header["X-User-Id"]; present {
		t.Fatalf("X-User-Id must be absent, got %q", cap.header.Get("X-User-Id"))
	}
}
