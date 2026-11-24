package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func agent(t *testing.T, state, answer string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t-9","status":{"state":"` + state + `"},"artifacts":[{"parts":[{"text":"` + answer + `"}]}]}}}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func do(t *testing.T, e map[string]string) (code int, stdout, stderr string) {
	t.Helper()
	var o, er bytes.Buffer
	code = run(env(e), &o, &er)
	return code, o.String(), er.String()
}

func TestRun_CompletedPrintsSummaryWithoutAnswer(t *testing.T) {
	s := agent(t, "TASK_STATE_COMPLETED", "SECRET-ANSWER")
	code, out, errs := do(t, map[string]string{"A2A_URL": s.URL, "A2A_USER_ID": "schedule:x", "PROMPT": "hi"})
	if code != 0 {
		t.Fatalf("code = %d stderr = %s", code, errs)
	}
	if strings.Contains(out+errs, "SECRET-ANSWER") {
		t.Fatalf("answer leaked: %s", out+errs)
	}
	var sum map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &sum); err != nil {
		t.Fatalf("stdout is not one JSON line: %q", out)
	}
	if sum["state"] != "TASK_STATE_COMPLETED" || sum["taskId"] != "t-9" || sum["answerChars"] != float64(len("SECRET-ANSWER")) {
		t.Fatalf("summary = %v", sum)
	}
	if _, ok := sum["durationMs"]; !ok {
		t.Fatalf("summary lacks durationMs: %v", sum)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("summary must be one line: %q", out)
	}
}

func TestRun_PrintAnswer(t *testing.T) {
	s := agent(t, "TASK_STATE_COMPLETED", "visible")
	code, out, _ := do(t, map[string]string{"A2A_URL": s.URL, "A2A_USER_ID": "u", "PROMPT": "hi", "PRINT_ANSWER": "true"})
	var sum map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &sum)
	if code != 0 || sum["answer"] != "visible" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestRun_PromptFromFileWins(t *testing.T) {
	var got string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		got = b["params"].(map[string]any)["message"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
		_, _ = w.Write([]byte(`{"result":{"task":{"id":"t","status":{"state":"TASK_STATE_COMPLETED"}}}}`))
	}))
	defer s.Close()
	f := filepath.Join(t.TempDir(), "p.txt")
	_ = os.WriteFile(f, []byte("from file\n"), 0o600)
	code, _, errs := do(t, map[string]string{"A2A_URL": s.URL, "A2A_USER_ID": "u", "PROMPT_FILE": f, "PROMPT": "from env"})
	if code != 0 || got != "from file" {
		t.Fatalf("code=%d got=%q err=%s", code, got, errs)
	}
}

func TestRun_FailedTaskExitsNonZeroWithMessage(t *testing.T) {
	s := agent(t, "TASK_STATE_FAILED", "partial")
	code, out, errs := do(t, map[string]string{"A2A_URL": s.URL, "A2A_USER_ID": "u", "PROMPT": "hi"})
	if code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errs, "TASK_STATE_FAILED") || strings.Contains(out+errs, "partial") {
		t.Fatalf("stderr = %q stdout = %q", errs, out)
	}
	var sum map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &sum)
	if sum["state"] != "TASK_STATE_FAILED" || sum["error"] == nil {
		t.Fatalf("summary = %v", sum)
	}
}

func TestRun_Timeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer s.Close()
	code, _, errs := do(t, map[string]string{"A2A_URL": s.URL, "A2A_USER_ID": "u", "PROMPT": "hi", "A2A_TIMEOUT": "150ms"})
	if code != 1 || !strings.Contains(errs, "timed out") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestRun_ConfigErrorsExitTwo(t *testing.T) {
	good := map[string]string{"A2A_URL": "http://127.0.0.1:1", "A2A_USER_ID": "u", "PROMPT": "hi"}
	cases := map[string]map[string]string{
		"no url":          {"A2A_USER_ID": "u", "PROMPT": "hi"},
		"no user":         {"A2A_URL": "http://x", "PROMPT": "hi"},
		"no prompt":       {"A2A_URL": "http://x", "A2A_USER_ID": "u"},
		"missing file":    {"A2A_URL": "http://x", "A2A_USER_ID": "u", "PROMPT_FILE": "/nonexistent/p"},
		"bad timeout":     {"A2A_URL": "http://x", "A2A_USER_ID": "u", "PROMPT": "hi", "A2A_TIMEOUT": "soon"},
		"zero timeout":    {"A2A_URL": "http://x", "A2A_USER_ID": "u", "PROMPT": "hi", "A2A_TIMEOUT": "0s"},
		"bad print flag":  {"A2A_URL": "http://x", "A2A_USER_ID": "u", "PROMPT": "hi", "PRINT_ANSWER": "maybe"},
		"bad max bytes":   {"A2A_URL": "http://x", "A2A_USER_ID": "u", "PROMPT": "hi", "MAX_RESPONSE_BYTES": "-1"},
		"non-http scheme": {"A2A_URL": "file:///etc/passwd", "A2A_USER_ID": "u", "PROMPT": "hi"},
	}
	_ = good
	for name, e := range cases {
		code, _, errs := do(t, e)
		if code != 2 || errs == "" {
			t.Errorf("%s: code=%d err=%q", name, code, errs)
		}
	}
}

func TestRun_DefaultTimeoutIs300s(t *testing.T) {
	if defaultTimeout.Seconds() != 300 {
		t.Fatalf("defaultTimeout = %v", defaultTimeout)
	}
}

func TestRun_SummaryCarriesTheTraceIDTheAgentWasSent(t *testing.T) {
	var seen string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("traceparent")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t-9","contextId":"c-7","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"parts":[{"text":"SECRET-ANSWER"}]}]}}}`))
	}))
	t.Cleanup(s.Close)
	code, out, errs := do(t, map[string]string{"A2A_URL": s.URL, "A2A_USER_ID": "schedule:x", "PROMPT": "SECRET-PROMPT"})
	if code != 0 {
		t.Fatalf("code %d %s", code, errs)
	}
	var sum map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &sum); err != nil {
		t.Fatalf("not one JSON line: %q", out)
	}
	parts := strings.Split(seen, "-")
	if len(parts) != 4 || sum["traceId"] != parts[1] {
		t.Fatalf("summary traceId %v, header %q", sum["traceId"], seen)
	}
	if sum["contextId"] != "c-7" {
		t.Fatalf("summary contextId = %v", sum["contextId"])
	}
	if strings.Contains(out+errs, "SECRET-PROMPT") || strings.Contains(out+errs, "SECRET-ANSWER") {
		t.Fatalf("text leaked into the output: %s %s", out, errs)
	}
}

func TestRun_FailureSummaryStillHasTheTraceID(t *testing.T) {
	s := agent(t, "TASK_STATE_FAILED", "x")
	code, out, _ := do(t, map[string]string{"A2A_URL": s.URL, "A2A_USER_ID": "schedule:x", "PROMPT": "hi"})
	var sum map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &sum)
	if code != 1 || len(strings.TrimSpace(asString(sum["traceId"]))) != 32 {
		t.Fatalf("code %d summary %v", code, sum)
	}
}

func TestRun_TraceparentEnvIsContinued(t *testing.T) {
	var seen string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("traceparent")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t","status":{"state":"TASK_STATE_COMPLETED"}}}}`))
	}))
	t.Cleanup(s.Close)
	_, out, _ := do(t, map[string]string{"A2A_URL": s.URL, "A2A_USER_ID": "u", "PROMPT": "hi", "TRACEPARENT": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"})
	if !strings.Contains(seen, "-4bf92f3577b34da6a3ce929d0e0e4736-") || !strings.Contains(out, `"traceId":"4bf92f3577b34da6a3ce929d0e0e4736"`) {
		t.Fatalf("header %q summary %s", seen, out)
	}
}

func asString(v any) string { s, _ := v.(string); return s }

func TestRun_TokenFileFromEnvAndMissingFile(t *testing.T) {
	var auth string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"parts":[{"text":"ok"}]}]}}}`))
	}))
	defer s.Close()
	f := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(f, []byte("tok-123"), 0o600)
	e := map[string]string{"A2A_URL": s.URL, "A2A_USER_ID": "schedule:x", "PROMPT": "hi", "A2A_TOKEN_FILE": f}
	if code, out, errs := do(t, e); code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
	if auth != "Bearer tok-123" {
		t.Fatalf("Authorization = %q", auth)
	}
	e["A2A_TOKEN_FILE"] = filepath.Join(t.TempDir(), "missing")
	auth = ""
	code, out, errs := do(t, e)
	if code != 1 || !strings.Contains(errs, "A2A_TOKEN_FILE") || auth != "" {
		t.Fatalf("code=%d err=%q sent=%q", code, errs, auth)
	}
	if strings.Contains(out, "tok-123") || strings.Contains(errs, "tok-123") {
		t.Fatal("the token leaked")
	}
}
