package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/openagentix/openagentix/bridges/telegram/internal/a2a"
)

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func (s *syncBuf) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func jsonLogger(buf *syncBuf, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
}

func turnRecords(t *testing.T, buf *syncBuf) []map[string]any {
	var out []map[string]any
	for _, r := range buf.records(t) {
		if r["msg"] == "turn" {
			out = append(out, r)
		}
	}
	return out
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)
var hex8 = regexp.MustCompile(`^[0-9a-f]{8}$`)

// traceAgent records the trace id the bridge put into the context of each Send.
type traceAgent struct {
	mu     sync.Mutex
	traces []string
	reply  string
	err    error
}

func (a *traceAgent) Send(ctx context.Context, ctxID, text string) (string, string, error) {
	tc, _ := a2a.TraceFromContext(ctx)
	a.mu.Lock()
	a.traces = append(a.traces, tc.TraceID)
	a.mu.Unlock()
	return a.reply, "ctx-A", a.err
}

func (a *traceAgent) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.traces...)
}

func TestObs_OneStructuredLinePerHandledMessage(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 424242, "my-secret-question-text")+"]")
	agent := &traceAgent{reply: "my-secret-answer-text"}
	var buf syncBuf
	b := newBridge(f, agent, NewMemoryStore())
	b.Log = jsonLogger(&buf, slog.LevelDebug)
	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 1 && len(turnRecords(t, &buf)) >= 1 })

	recs := turnRecords(t, &buf)
	if len(recs) != 1 {
		t.Fatalf("want exactly one turn line, got %d: %s", len(recs), buf.String())
	}
	r := recs[0]
	if r["outcome"] != "ok" {
		t.Fatalf("outcome = %v", r["outcome"])
	}
	tid, _ := r["trace_id"].(string)
	if !hex32.MatchString(tid) {
		t.Fatalf("trace_id %q", tid)
	}
	if got := agent.seen(); len(got) != 1 || got[0] != tid {
		t.Fatalf("the trace id in the log (%s) is not the one sent to the agent (%v)", tid, got)
	}
	if h, _ := r["chat_hash"].(string); !hex8.MatchString(h) {
		t.Fatalf("chat_hash %v is not a short hash", r["chat_hash"])
	}
	if r["context_id"] != "ctx-A" {
		t.Fatalf("context_id = %v", r["context_id"])
	}
	for _, k := range []string{"agent_ms", "total_ms", "reply_chars"} {
		if _, ok := r[k].(float64); !ok {
			t.Fatalf("field %s missing or not a number: %v", k, r[k])
		}
	}
	if r["reply_chars"] != float64(len("my-secret-answer-text")) {
		t.Fatalf("reply_chars = %v", r["reply_chars"])
	}
}

func TestObs_NeverLogsTextOrRawChatIDs(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 424242, "my-secret-question-text")+"]", "["+upd(2, 424242, "/new")+"]", "["+upd(3, 777001, "other chat text")+"]")
	agent := &traceAgent{reply: "my-secret-answer-text"}
	var buf syncBuf
	b := newBridge(f, agent, NewMemoryStore())
	b.AllowAll = false
	b.Allowed = map[int64]bool{424242: true} // 777001 is rejected: its (debug) log line must not carry the id either
	b.Log = jsonLogger(&buf, slog.LevelDebug)
	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 2 && len(f.offsetsSeen()) >= 4 })
	out := buf.String()
	for _, secret := range []string{"my-secret-question-text", "my-secret-answer-text", "other chat text", "424242", "777001"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log output contains %q:\n%s", secret, out)
		}
	}
}

func TestObs_ErrorPathsDoNotLogRawChatID(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 424242, "question")+"]")
	agent := &traceAgent{err: errors.New("a2a: agent returned HTTP 500")}
	var buf syncBuf
	b := newBridge(f, agent, NewMemoryStore())
	b.Log = jsonLogger(&buf, slog.LevelDebug)
	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 1 && len(turnRecords(t, &buf)) >= 1 })
	if strings.Contains(buf.String(), "424242") {
		t.Fatalf("raw chat id in log:\n%s", buf.String())
	}
	r := turnRecords(t, &buf)[0]
	if r["outcome"] != "agent_error" {
		t.Fatalf("outcome = %v", r["outcome"])
	}
	if !hex32.MatchString(r["trace_id"].(string)) {
		t.Fatalf("a failed turn still has a trace id: %v", r["trace_id"])
	}
}

func TestObs_EachTurnGetsItsOwnTrace(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "one")+"]", "["+upd(2, 5, "two")+"]")
	agent := &traceAgent{reply: "r"}
	b := newBridge(f, agent, NewMemoryStore())
	runUntil(t, b, func() bool { return len(agent.seen()) >= 2 && len(f.sentMsgs()) >= 2 })
	got := agent.seen()
	if got[0] == got[1] || !hex32.MatchString(got[0]) || !hex32.MatchString(got[1]) {
		t.Fatalf("trace ids = %v", got)
	}
}

func TestObs_NewCommandIsLoggedWithoutAgentCall(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "/new")+"]")
	agent := &traceAgent{reply: "r"}
	var buf syncBuf
	b := newBridge(f, agent, NewMemoryStore())
	b.Log = jsonLogger(&buf, slog.LevelInfo)
	runUntil(t, b, func() bool { return len(turnRecords(t, &buf)) >= 1 })
	r := turnRecords(t, &buf)[0]
	if r["outcome"] != "new_conversation" || len(agent.seen()) != 0 {
		t.Fatalf("record %v, agent calls %v", r, agent.seen())
	}
}

func TestObs_ChatHashIsStableWithinAProcessAndSaltedAcrossBridges(t *testing.T) {
	a, b := &Bridge{}, &Bridge{}
	if a.chatHash(42) != a.chatHash(42) || a.chatHash(42) == a.chatHash(43) {
		t.Fatal("hash must be stable per chat and differ between chats")
	}
	if a.chatHash(42) == b.chatHash(42) {
		t.Fatal("two bridges (two process lifetimes) share a salt: the hash could be matched against the small chat id space offline")
	}
	if !hex8.MatchString(a.chatHash(42)) {
		t.Fatalf("hash = %q", a.chatHash(42))
	}
}
