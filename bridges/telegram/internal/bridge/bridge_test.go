package bridge

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openagentix/openagentix/bridges/telegram/internal/a2a"
	"github.com/openagentix/openagentix/bridges/telegram/internal/telegram"
)

// ---- fake Telegram (httptest) ----

type fakeTelegram struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	batches    []string // raw JSON "result" arrays handed out one per getUpdates
	offsets    []int64
	sent       []sentMsg
	events     []string // ordered "typing" / "send" markers
	failTyping bool
	failNext   int // number of getUpdates calls to fail
	failStatus int // HTTP status for those failures; 0 means 500
	getCalls   int
}

type sentMsg struct {
	ChatID int64
	Text   string
}

func newFakeTelegram(t *testing.T, batches ...string) *fakeTelegram {
	f := &fakeTelegram{t: t, batches: batches}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTelegram) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		f.getCalls++
		f.offsets = append(f.offsets, int64(body["offset"].(float64)))
		if f.failNext > 0 {
			f.failNext--
			code := cmp.Or(f.failStatus, 500)
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"ok":false,"description":"boom"}`)
			return
		}
		if len(f.batches) == 0 {
			time.Sleep(5 * time.Millisecond)
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
			return
		}
		b := f.batches[0]
		f.batches = f.batches[1:]
		_, _ = io.WriteString(w, `{"ok":true,"result":`+b+`}`)
	case strings.HasSuffix(r.URL.Path, "/getMe"):
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"My_Bot"}}`)
	case strings.HasSuffix(r.URL.Path, "/sendChatAction"):
		f.events = append(f.events, fmt.Sprintf("typing:%v:%v", body["chat_id"], body["action"]))
		if f.failTyping {
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"ok":false,"description":"typing boom"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		f.events = append(f.events, "send")
		f.sent = append(f.sent, sentMsg{int64(body["chat_id"].(float64)), body["text"].(string)})
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	default:
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}
}

func (f *fakeTelegram) sentMsgs() []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMsg(nil), f.sent...)
}

func (f *fakeTelegram) offsetsSeen() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.offsets...)
}

func upd(id, chat int64, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":%d},"text":%q}}`, id, id, chat, text)
}

// ---- stub agent ----

type agentCall struct{ ContextID, Text string }

type stubAgent struct {
	mu    sync.Mutex
	calls []agentCall
	fn    func(n int, ctxID, text string) (string, string, error)
}

func (a *stubAgent) Send(_ context.Context, ctxID, text string) (string, string, error) {
	a.mu.Lock()
	a.calls = append(a.calls, agentCall{ctxID, text})
	n := len(a.calls)
	a.mu.Unlock()
	return a.fn(n, ctxID, text)
}

func (a *stubAgent) snapshot() []agentCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]agentCall(nil), a.calls...)
}

// ---- harness ----

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// runUntil starts the bridge and cancels it once cond is true (or fails on timeout).
func runUntil(t *testing.T, b *Bridge, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("condition not reached before timeout")
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func newBridge(f *fakeTelegram, agent Agent, store ContextStore) *Bridge {
	return &Bridge{
		Telegram:     telegram.NewClient(f.srv.URL, "TOKEN123", f.srv.Client()),
		Agent:        agent,
		Store:        store,
		Log:          discardLogger(),
		AgentTimeout: time.Second,
		PollTimeout:  0,
		Backoff:      time.Millisecond,
		AllowAll:     true,
	}
}

func TestBridge_ForwardsTextAndRepliesWithContextReuse(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(100, 42, "hello")+"]", "["+upd(101, 42, "again")+"]")
	agent := &stubAgent{fn: func(n int, ctxID, text string) (string, string, error) {
		return "reply " + text, "ctx-A", nil
	}}
	b := newBridge(f, agent, NewMemoryStore())

	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 2 && len(f.offsetsSeen()) >= 3 })

	calls := agent.snapshot()
	if len(calls) != 2 || calls[0] != (agentCall{"", "hello"}) || calls[1] != (agentCall{"ctx-A", "again"}) {
		t.Fatalf("agent calls = %+v", calls)
	}
	sent := f.sentMsgs()
	if sent[0] != (sentMsg{42, "reply hello"}) || sent[1] != (sentMsg{42, "reply again"}) {
		t.Fatalf("sent = %+v", sent)
	}
	offs := f.offsetsSeen()
	if offs[0] != 0 || offs[1] != 101 || offs[2] != 102 {
		t.Fatalf("offsets = %v", offs)
	}
}

func okAgent() *stubAgent {
	return &stubAgent{fn: func(n int, ctxID, text string) (string, string, error) {
		return "r:" + text, "ctx-A", nil
	}}
}

func TestBridge_NewCommandResetsContext(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "one")+"]", "["+upd(2, 5, "/new")+"]", "["+upd(3, 5, "two")+"]")
	agent := okAgent()
	store := NewMemoryStore()
	b := newBridge(f, agent, store)

	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 3 })

	calls := agent.snapshot()
	if len(calls) != 2 || calls[1] != (agentCall{"", "two"}) {
		t.Fatalf("agent calls = %+v (/new must not reach agent, and next turn must have no context)", calls)
	}
	sent := f.sentMsgs()
	if sent[1].ChatID != 5 || sent[1].Text == "" {
		t.Fatalf("expected a confirmation for /new, got %+v", sent[1])
	}
}

func TestBridge_IgnoresNonTextUpdates(t *testing.T) {
	f := newFakeTelegram(t,
		`[{"update_id":1,"message":{"message_id":1,"chat":{"id":5},"photo":[{}]}},{"update_id":2,"edited_message":{"message_id":1,"chat":{"id":5},"text":"x"}},`+upd(3, 5, "real")+`]`)
	agent := okAgent()
	runUntil(t, newBridge(f, agent, NewMemoryStore()), func() bool { return len(f.sentMsgs()) >= 1 })

	calls := agent.snapshot()
	if len(calls) != 1 || calls[0].Text != "real" {
		t.Fatalf("agent calls = %+v", calls)
	}
}

func TestBridge_AllowlistIgnoresOtherChats(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 666, "intruder")+","+upd(2, 5, "friend")+"]")
	agent := okAgent()
	b := newBridge(f, agent, NewMemoryStore())
	b.AllowAll = false
	b.Allowed = map[int64]bool{5: true}

	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 1 })

	calls := agent.snapshot()
	if len(calls) != 1 || calls[0].Text != "friend" {
		t.Fatalf("agent calls = %+v", calls)
	}
	for _, s := range f.sentMsgs() {
		if s.ChatID == 666 {
			t.Fatal("must not reply to disallowed chat")
		}
	}
	// offset must still advance past the ignored update
	if offs := f.offsetsSeen(); offs[len(offs)-1] != 3 {
		t.Fatalf("offsets = %v", offs)
	}
}

func TestBridge_AgentErrorRepliesGenericAndAdvancesOffset(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(10, 5, "bad")+"]", "["+upd(11, 5, "good")+"]")
	agent := &stubAgent{fn: func(n int, ctxID, text string) (string, string, error) {
		if n == 1 {
			return "", "", errors.New("dial tcp 10.0.0.7:8080: secret-internal-detail")
		}
		return "fine", "ctx-A", nil
	}}
	var logbuf strings.Builder
	b := newBridge(f, agent, NewMemoryStore())
	b.Log = slog.New(slog.NewTextHandler(&logbuf, nil))

	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 2 })

	sent := f.sentMsgs()
	if sent[0].ChatID != 5 || sent[0].Text == "" || strings.Contains(sent[0].Text, "secret-internal-detail") || strings.Contains(sent[0].Text, "10.0.0.7") {
		t.Fatalf("failure reply = %q", sent[0].Text)
	}
	if sent[1].Text != "fine" {
		t.Fatalf("bridge stuck after error: %+v", sent)
	}
	if !strings.Contains(logbuf.String(), "secret-internal-detail") {
		t.Fatalf("real error must be logged, log = %s", logbuf.String())
	}
	// the bad update must never be re-fetched
	for _, o := range f.offsetsSeen() {
		if o == 10 {
			t.Fatalf("update 10 re-polled: offsets = %v", f.offsetsSeen())
		}
	}
}

func TestBridge_GetUpdatesFailureBacksOffAndRetries(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "hi")+"]")
	f.failNext = 3
	agent := okAgent()
	b := newBridge(f, agent, NewMemoryStore())
	b.Backoff = 30 * time.Millisecond

	start := time.Now()
	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 1 })

	if el := time.Since(start); el < 60*time.Millisecond {
		t.Fatalf("no backoff between failed polls: %v", el)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getCalls < 4 {
		t.Fatalf("getCalls = %d", f.getCalls)
	}
}

func TestBridge_ErrorDoesNotClearExistingContext(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "a")+"]", "["+upd(2, 5, "b")+"]", "["+upd(3, 5, "c")+"]")
	agent := &stubAgent{fn: func(n int, ctxID, text string) (string, string, error) {
		if n == 2 {
			return "", "", errors.New("down")
		}
		return "ok", "ctx-A", nil
	}}
	runUntil(t, newBridge(f, agent, NewMemoryStore()), func() bool { return len(f.sentMsgs()) >= 3 })
	if c := agent.snapshot()[2]; c.ContextID != "ctx-A" {
		t.Fatalf("third call = %+v", c)
	}
}

func TestBridge_NewCommandWithBotSuffix(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "one")+"]", "["+upd(2, 5, "/new@my_bot")+"]", "["+upd(3, 5, "two")+"]")
	agent := okAgent()
	runUntil(t, newBridge(f, agent, NewMemoryStore()), func() bool { return len(f.sentMsgs()) >= 3 })
	if calls := agent.snapshot(); len(calls) != 2 || calls[1].ContextID != "" {
		t.Fatalf("agent calls = %+v", calls)
	}
}

func TestBridge_ShutdownInterruptsInFlightAgentCall(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "slow")+"]")
	started := make(chan struct{})
	blocking := agentFunc(func(ctx context.Context, _, _ string) (string, string, error) {
		close(started)
		<-ctx.Done()
		return "", "", ctx.Err()
	})
	b := newBridge(f, blocking, NewMemoryStore())
	b.AgentTimeout = time.Minute

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly on shutdown")
	}
	for _, s := range f.sentMsgs() {
		t.Fatalf("no reply expected on shutdown, got %+v", s)
	}
}

type agentFunc func(ctx context.Context, contextID, text string) (string, string, error)

func (f agentFunc) Send(ctx context.Context, c, t string) (string, string, error) {
	return f(ctx, c, t)
}

func (f *fakeTelegram) eventLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func TestBridge_SendsTypingBeforeAgentCall(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 42, "hello")+"]")
	var typingSeenByAgent []string
	var b *Bridge
	agent := &stubAgent{fn: func(n int, ctxID, text string) (string, string, error) {
		typingSeenByAgent = f.eventLog()
		return "ok", "c", nil
	}}
	b = newBridge(f, agent, NewMemoryStore())
	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 1 })
	if len(typingSeenByAgent) != 1 || typingSeenByAgent[0] != "typing:42:typing" {
		t.Fatalf("typing must be sent before agent call; events at agent call = %v", typingSeenByAgent)
	}
}

func TestBridge_TypingFailureDoesNotBlockTurn(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 42, "hello")+"]")
	f.failTyping = true
	b := newBridge(f, okAgent(), NewMemoryStore())
	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 1 })
	if got := f.sentMsgs()[0].Text; got == "" || got == genericFailure {
		t.Fatalf("reply = %q", got)
	}
}

func TestBridge_EmptyAllowlistFailsClosedAndLogsAtDebug(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 666, "intruder")+"]")
	agent := okAgent()
	var logbuf strings.Builder
	b := newBridge(f, agent, NewMemoryStore())
	b.AllowAll = false
	b.Allowed = nil
	b.Log = slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	runUntil(t, b, func() bool { offs := f.offsetsSeen(); return len(offs) >= 2 && offs[len(offs)-1] == 2 })

	if len(agent.snapshot()) != 0 || len(f.sentMsgs()) != 0 {
		t.Fatalf("empty allowlist must reject everyone: calls=%v sent=%v", agent.snapshot(), f.sentMsgs())
	}
	if strings.Contains(logbuf.String(), "666") {
		t.Fatalf("rejected chats must be logged at Debug, got: %s", logbuf.String())
	}
}

func TestBridge_SlowChatDoesNotBlockOtherChats(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "slow")+","+upd(2, 6, "fast")+"]")
	agent := agentFunc(func(ctx context.Context, _, text string) (string, string, error) {
		if text == "slow" {
			<-ctx.Done() // never finishes on its own
			return "", "", ctx.Err()
		}
		return "r:" + text, "", nil
	})
	b := newBridge(f, agent, NewMemoryStore())
	b.AgentTimeout = 5 * time.Second

	runUntil(t, b, func() bool {
		for _, s := range f.sentMsgs() {
			if s.ChatID == 6 {
				return true
			}
		}
		return false
	})
}

func TestBridge_SameChatTurnsStayOrdered(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "a")+","+upd(2, 5, "b")+","+upd(3, 5, "c")+"]")
	agent := &stubAgent{fn: func(n int, ctxID, text string) (string, string, error) {
		if text == "a" {
			time.Sleep(60 * time.Millisecond)
		}
		return "r:" + text, "ctx-" + text, nil
	}}
	runUntil(t, newBridge(f, agent, NewMemoryStore()), func() bool { return len(f.sentMsgs()) >= 3 })

	calls := agent.snapshot()
	want := []agentCall{{"", "a"}, {"ctx-a", "b"}, {"ctx-b", "c"}}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %+v, want %+v", calls, want)
		}
	}
	sent := f.sentMsgs()
	if sent[0].Text != "r:a" || sent[1].Text != "r:b" || sent[2].Text != "r:c" {
		t.Fatalf("reply order = %+v", sent)
	}
}

func TestBridge_ConcurrentTurnsAreBounded(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 1, "x")+","+upd(2, 2, "x")+","+upd(3, 3, "x")+","+upd(4, 4, "x")+","+upd(5, 5, "x")+"]")
	var mu sync.Mutex
	cur, peak := 0, 0
	agent := &stubAgent{fn: func(n int, ctxID, text string) (string, string, error) {
		mu.Lock()
		cur++
		peak = max(peak, cur)
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
		return "ok", "", nil
	}}
	b := newBridge(f, agent, NewMemoryStore())
	b.MaxConcurrentTurns = 2
	runUntil(t, b, func() bool { return len(f.sentMsgs()) >= 5 })
	mu.Lock()
	defer mu.Unlock()
	if peak != 2 {
		t.Fatalf("peak concurrent turns = %d, want 2", peak)
	}
}

func TestBridge_ShutdownWaitsForInFlightTurnWithinGrace(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "slow")+"]")
	started := make(chan struct{})
	agent := &stubAgent{fn: func(n int, ctxID, text string) (string, string, error) {
		close(started)
		time.Sleep(100 * time.Millisecond)
		return "finished", "", nil
	}}
	b := newBridge(f, agent, NewMemoryStore())
	b.ShutdownGrace = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	<-started
	cancel()
	<-done
	if s := f.sentMsgs(); len(s) != 1 || s[0].Text != "finished" {
		t.Fatalf("in-flight turn must complete and reply during grace, sent = %+v", s)
	}
}

func TestBridge_ShutdownGraceIsBounded(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "stuck")+"]")
	started := make(chan struct{})
	agent := agentFunc(func(ctx context.Context, _, _ string) (string, string, error) {
		close(started)
		<-ctx.Done()
		return "", "", ctx.Err()
	})
	b := newBridge(f, agent, NewMemoryStore())
	b.AgentTimeout = time.Minute
	b.ShutdownGrace = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return after the grace period")
	}
}

func TestBridge_ShutdownMidBatchStopsProcessingRemainingUpdates(t *testing.T) {
	f := newFakeTelegram(t, "["+upd(1, 5, "a")+","+upd(2, 5, "b")+","+upd(3, 5, "c")+"]")
	ctx, cancel := context.WithCancel(context.Background())
	agent := &stubAgent{fn: func(n int, ctxID, text string) (string, string, error) {
		cancel() // shutdown arrives during the first turn
		return "r:" + text, "", nil
	}}
	var logbuf strings.Builder
	b := newBridge(f, agent, NewMemoryStore())
	b.Log = slog.New(slog.NewTextHandler(&logbuf, nil))
	b.ShutdownGrace = time.Second

	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
	if n := len(agent.snapshot()); n != 1 {
		t.Fatalf("agent calls after shutdown = %d, want 1", n)
	}
	if strings.Contains(logbuf.String(), "level=ERROR") {
		t.Fatalf("shutdown must not log failures: %s", logbuf.String())
	}
}

func TestBridge_HealthzReflectsPollFreshness(t *testing.T) {
	f := newFakeTelegram(t)
	var clock atomic.Int64
	base := time.Unix(1_000_000, 0)
	clock.Store(base.UnixNano())
	b := newBridge(f, okAgent(), NewMemoryStore())
	b.PollTimeout = 10
	b.Now = func() time.Time { return time.Unix(0, clock.Load()) }

	get := func() int {
		rec := httptest.NewRecorder()
		b.HealthHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
		return rec.Code
	}
	if c := get(); c != 503 {
		t.Fatalf("before the first successful poll = %d, want 503", c)
	}
	runUntil(t, b, func() bool { return len(f.offsetsSeen()) >= 2 })
	if c := get(); c != 200 {
		t.Fatalf("after a successful poll = %d, want 200", c)
	}
	clock.Store(base.Add(29 * time.Second).UnixNano())
	if c := get(); c != 200 {
		t.Fatalf("within 3x poll timeout = %d, want 200", c)
	}
	clock.Store(base.Add(31 * time.Second).UnixNano())
	if c := get(); c != 503 {
		t.Fatalf("beyond 3x poll timeout = %d, want 503", c)
	}
	rec := httptest.NewRecorder()
	b.HealthHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/other", nil))
	if rec.Code != 404 {
		t.Fatalf("unknown path = %d", rec.Code)
	}
}

func TestBridge_UnauthorizedFromTelegramIsFatal(t *testing.T) {
	f := newFakeTelegram(t)
	f.failNext, f.failStatus = 1000, 401
	b := newBridge(f, okAgent(), NewMemoryStore())
	b.Backoff = 5 * time.Millisecond

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background()) }()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "TOKEN123") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a 401 must stop Run instead of retrying forever")
	}
	if n := len(f.offsetsSeen()); n != 1 {
		t.Fatalf("polled %d times after 401", n)
	}
}

func TestNextBackoff_DoublesUpToCap(t *testing.T) {
	want := []time.Duration{1, 2, 4, 8, 10, 10}
	for i, w := range want {
		if got := nextBackoff(time.Second, 10*time.Second, i); got != w*time.Second {
			t.Fatalf("failures=%d: got %v, want %v", i, got, w*time.Second)
		}
	}
	if got := nextBackoff(time.Second, 0, 5); got != time.Second {
		t.Fatalf("no cap configured must keep base, got %v", got)
	}
	if got := nextBackoff(time.Second, time.Minute, 200); got != time.Minute {
		t.Fatalf("overflow guard: got %v", got)
	}
}

func TestBridge_NewCommandOnlyForOwnUsername(t *testing.T) {
	for _, tc := range []struct {
		cmd   string
		reset bool
	}{
		{"/new", true}, {"/new@my_bot", true}, {"/new@MY_BOT extra", true},
		{"/new@OtherBot", false}, {"/newer", false}, {"/new@my_bot2", false},
	} {
		f := newFakeTelegram(t, "["+upd(1, 5, "one")+"]", "["+upd(2, 5, tc.cmd)+"]", "["+upd(3, 5, "two")+"]")
		agent := okAgent()
		runUntil(t, newBridge(f, agent, NewMemoryStore()), func() bool { return len(f.sentMsgs()) >= 3 })
		calls := agent.snapshot()
		last := calls[len(calls)-1]
		if tc.reset && last.ContextID != "" || !tc.reset && last.ContextID != "ctx-A" {
			t.Fatalf("%q: reset=%v but last agent call = %+v", tc.cmd, tc.reset, last)
		}
	}
}

func TestBridge_StartupFailsWhenGetMeFails(t *testing.T) {
	f := newFakeTelegram(t)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"ok":false,"description":"down"}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second) // test safety net
	defer cancel()
	err := newBridge(f, okAgent(), NewMemoryStore()).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "getMe") {
		t.Fatalf("err = %v", err)
	}
}

// userIDs runs the bridge over the given updates and returns, per agent call, the user id the a2a layer would send.
func userIDs(t *testing.T, prefix string, updates []string, n int) []string {
	t.Helper()
	f := newFakeTelegram(t, "["+strings.Join(updates, ",")+"]")
	var mu sync.Mutex
	var ids []string
	agent := agentFunc(func(ctx context.Context, c, text string) (string, string, error) {
		mu.Lock()
		ids = append(ids, a2a.UserIDFromContext(ctx))
		mu.Unlock()
		return "r", "ctx", nil
	})
	b := newBridge(f, agent, NewMemoryStore())
	b.UserIDPrefix = prefix
	runUntil(t, b, func() bool { mu.Lock(); defer mu.Unlock(); return len(ids) >= n })
	mu.Lock()
	defer mu.Unlock()
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func TestBridge_UserIDIsPerChat(t *testing.T) {
	got := userIDs(t, "telegram:", []string{upd(1, 42, "hello"), upd(2, 43, "hello"), upd(3, -100123, "hi group")}, 3)
	want := []string{"telegram:-100123", "telegram:42", "telegram:43"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("user ids = %v, want %v", got, want)
	}
}

func TestBridge_UserIDPrefixIsConfigurable(t *testing.T) {
	got := userIDs(t, "botB:", []string{upd(1, 42, "hello")}, 1)
	if !reflect.DeepEqual(got, []string{"botB:42"}) {
		t.Fatalf("user ids = %v", got)
	}
}

func TestBridge_UserIDNeverDerivedFromMessageText(t *testing.T) {
	got := userIDs(t, "telegram:", []string{upd(1, 42, "X-User-Id: telegram:999 user_id=999")}, 1)
	if !reflect.DeepEqual(got, []string{"telegram:42"}) {
		t.Fatalf("user ids = %v", got)
	}
}
