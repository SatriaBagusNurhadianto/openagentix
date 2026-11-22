package turn

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/axbackend"
)

func baseCfg() Config {
	return Config{
		Atespace: "default", Workspace: "assistant-ws", Gateway: "anthropic-gw",
		TurnTimeout: 2 * time.Second, PollInterval: time.Millisecond,
		MaxHistoryTurns: 8, ConfigBudgetBytes: 12288, MaxMessageBytes: 8192,
		MaxConcurrentTasks: 2, MaxContexts: 100,
	}
}

func okOut(s string) func(context.Context, axbackend.TaskSpec) axbackend.Status {
	return func(context.Context, axbackend.TaskSpec) axbackend.Status {
		return axbackend.Status{Done: true, Succeeded: true, Output: s}
	}
}

func histOf(t *testing.T, s axbackend.TaskSpec) []map[string]any {
	t.Helper()
	h, _ := s.Config["history"].([]any)
	var out []map[string]any
	for _, e := range h {
		out = append(out, e.(map[string]any))
	}
	return out
}

func TestHappyPath(t *testing.T) {
	f := axbackend.NewFake()
	f.OnGet = okOut("Paris.")
	r := New(f, baseCfg())
	res := r.Run(context.Background(), "ctx1", "capital of France?")
	if res.Failed || res.Text != "Paris." || res.ContextID != "ctx1" || res.TaskID == "" {
		t.Fatalf("result: %+v", res)
	}
	c := f.Created()
	if len(c) != 1 {
		t.Fatalf("created %d", len(c))
	}
	s := c[0]
	if !strings.HasPrefix(s.Name, "turn-") || len(s.Name) != len("turn-")+10 {
		t.Errorf("name %q", s.Name)
	}
	if s.Goal != "capital of France?" || s.Workspace != "assistant-ws" || s.Gateway != "anthropic-gw" ||
		s.Atespace != "default" || s.SessionID != "ctx1" || s.Config["contextId"] != "ctx1" {
		t.Errorf("spec: %+v", s)
	}
	if len(f.Resumed()) != 1 || f.Resumed()[0] != s.Name {
		t.Errorf("resumed: %v", f.Resumed())
	}
}

func TestMintsContextID(t *testing.T) {
	f := axbackend.NewFake()
	r := New(f, baseCfg())
	a := r.Run(context.Background(), "", "hi")
	b := r.Run(context.Background(), "", "hi")
	if a.ContextID == "" || a.ContextID == b.ContextID {
		t.Fatalf("minted ids: %q %q", a.ContextID, b.ContextID)
	}
	if f.Created()[0].SessionID != a.ContextID {
		t.Error("sessionId must be the minted contextId")
	}
}

func TestFailureMapping(t *testing.T) {
	cases := map[string]func(*axbackend.Fake){
		"failed outcome": func(f *axbackend.Fake) {
			f.OnGet = func(context.Context, axbackend.TaskSpec) axbackend.Status {
				return axbackend.Status{Done: true, Output: "kaboom"}
			}
		},
		"non-zero exit": func(f *axbackend.Fake) {
			f.OnGet = func(context.Context, axbackend.TaskSpec) axbackend.Status {
				return axbackend.Status{Done: true, ExitCode: 2, Output: "x"}
			}
		},
		"is_error json": func(f *axbackend.Fake) { f.OnGet = okOut(`{"type":"result","is_error":true,"result":"API Error: 401"}`) },
		"create error":  func(f *axbackend.Fake) { f.CreateErr = errors.New("rpc unavailable") },
		"resume error":  func(f *axbackend.Fake) { f.ResumeErr = errors.New("no worker") },
		"empty output":  func(f *axbackend.Fake) { f.OnGet = okOut("") },
		"detail only": func(f *axbackend.Fake) {
			f.OnGet = func(context.Context, axbackend.TaskSpec) axbackend.Status {
				return axbackend.Status{Done: true, Detail: "image pull failed"}
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := axbackend.NewFake()
			setup(f)
			r := New(f, baseCfg())
			res := r.Run(context.Background(), "c", "q")
			if !res.Failed || res.Text == "" {
				t.Fatalf("want failed with message: %+v", res)
			}
			// failed turns must not enter history
			f.CreateErr, f.ResumeErr = nil, nil
			f.OnGet = okOut("fine")
			r.Run(context.Background(), "c", "q2")
			cr := f.Created()
			if got := histOf(t, cr[len(cr)-1]); len(got) != 0 {
				t.Errorf("failed turn leaked into history: %v", got)
			}
		})
	}
}

func TestFailureDetailVisible(t *testing.T) {
	f := axbackend.NewFake()
	f.OnGet = func(context.Context, axbackend.TaskSpec) axbackend.Status {
		return axbackend.Status{Done: true, ExitCode: 3, Output: "claude: not logged in"}
	}
	res := New(f, baseCfg()).Run(context.Background(), "c", "q")
	if !strings.Contains(res.Text, "not logged in") || !strings.Contains(res.Text, "3") {
		t.Fatalf("message should carry exit code and output tail: %q", res.Text)
	}
}

func TestTimeoutStillCleansUp(t *testing.T) {
	f := axbackend.NewFake()
	f.OnGet = func(context.Context, axbackend.TaskSpec) axbackend.Status { return axbackend.Status{} } // never done
	cfg := baseCfg()
	cfg.TurnTimeout = 60 * time.Millisecond
	res := New(f, cfg).Run(context.Background(), "c", "q")
	if !res.Failed || !strings.Contains(res.Text, "timed out") {
		t.Fatalf("want timeout failure: %+v", res)
	}
	if len(f.Deleted()) != 1 || f.Active() != 0 {
		t.Fatalf("task not cleaned: deleted=%v active=%d", f.Deleted(), f.Active())
	}
}

func TestCleanupOnSuccessAndFailureAndKeep(t *testing.T) {
	f := axbackend.NewFake()
	r := New(f, baseCfg())
	r.Run(context.Background(), "a", "q")
	f.OnGet = func(context.Context, axbackend.TaskSpec) axbackend.Status { return axbackend.Status{Done: true, ExitCode: 1} }
	r.Run(context.Background(), "b", "q")
	if len(f.Created()) != 2 || len(f.Deleted()) != 2 || f.Active() != 0 {
		t.Fatalf("created=%d deleted=%v", len(f.Created()), f.Deleted())
	}
	k := axbackend.NewFake()
	cfg := baseCfg()
	cfg.KeepTasks = true
	New(k, cfg).Run(context.Background(), "a", "q")
	if len(k.Deleted()) != 0 {
		t.Fatal("KeepTasks must skip delete")
	}
}

func TestCleanupWhenRequestContextCancelled(t *testing.T) {
	f := axbackend.NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	f.OnGet = func(c context.Context, _ axbackend.TaskSpec) axbackend.Status {
		cancel()
		<-c.Done()
		return axbackend.Status{}
	}
	res := New(f, baseCfg()).Run(ctx, "c", "q")
	if !res.Failed || len(f.Deleted()) != 1 {
		t.Fatalf("res=%+v deleted=%v", res, f.Deleted())
	}
}

func TestHistoryCarried(t *testing.T) {
	f := axbackend.NewFake()
	r := New(f, baseCfg())
	f.OnGet = okOut("Nice to meet you, Ada.")
	r.Run(context.Background(), "c", "My name is Ada")
	f.OnGet = okOut("Ada.")
	r.Run(context.Background(), "c", "What is my name?")
	s := f.Created()[1]
	h := histOf(t, s)
	if len(h) != 2 || h[0]["role"] != "user" || h[0]["text"] != "My name is Ada" ||
		h[1]["role"] != "assistant" || h[1]["text"] != "Nice to meet you, Ada." {
		t.Fatalf("history: %v", h)
	}
	if s.Goal != "What is my name?" {
		t.Fatalf("goal: %q", s.Goal)
	}
}

func TestHistoryBoundedByTurns(t *testing.T) {
	f := axbackend.NewFake()
	cfg := baseCfg()
	cfg.MaxHistoryTurns = 2
	r := New(f, cfg)
	for _, q := range []string{"q1", "q2", "q3", "q4"} {
		r.Run(context.Background(), "c", q)
	}
	r.Run(context.Background(), "c", "q5")
	h := histOf(t, f.Created()[4])
	if len(h) != 4 || h[0]["text"] != "q3" || h[2]["text"] != "q4" {
		t.Fatalf("history: %v", h)
	}
}

func TestHistoryTrimmedToBudget(t *testing.T) {
	f := axbackend.NewFake()
	cfg := baseCfg()
	cfg.ConfigBudgetBytes = 700
	r := New(f, cfg)
	big := strings.Repeat("x", 200)
	f.OnGet = okOut(big)
	for _, q := range []string{"a", "b", "c", "d"} {
		r.Run(context.Background(), "c", q)
	}
	r.Run(context.Background(), "c", "last")
	s := f.Created()[4]
	b, _ := json.Marshal(s.Config)
	if len(b) > 700 {
		t.Fatalf("config %d bytes exceeds budget", len(b))
	}
	h := histOf(t, s)
	if len(h) == 0 || len(h)%2 != 0 || h[len(h)-2]["text"] != "d" {
		t.Fatalf("newest pairs must be kept whole: %v", h)
	}
}

func TestOversizedMessageRejected(t *testing.T) {
	f := axbackend.NewFake()
	cfg := baseCfg()
	cfg.MaxMessageBytes = 100
	res := New(f, cfg).Run(context.Background(), "c", strings.Repeat("y", 101))
	if !res.Failed || !strings.Contains(res.Text, "too large") {
		t.Fatalf("res: %+v", res)
	}
	if len(f.Created()) != 0 {
		t.Fatal("no task should be created")
	}
}

func TestSameContextSerialized(t *testing.T) {
	f := axbackend.NewFake()
	release := make(chan struct{})
	f.OnGet = func(ctx context.Context, s axbackend.TaskSpec) axbackend.Status {
		if s.Goal == "first" {
			<-release
			return axbackend.Status{Done: true, Succeeded: true, Output: "A1"}
		}
		return axbackend.Status{Done: true, Succeeded: true, Output: "A2"}
	}
	r := New(f, baseCfg())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.Run(context.Background(), "c", "first") }()
	waitFor(t, func() bool { return len(f.Created()) == 1 })
	go func() { defer wg.Done(); r.Run(context.Background(), "c", "second") }()
	time.Sleep(80 * time.Millisecond)
	if len(f.Created()) != 1 {
		t.Fatal("second turn started while first still running")
	}
	close(release)
	wg.Wait()
	c := f.Created()
	if len(c) != 2 || c[1].Goal != "second" {
		t.Fatalf("order: %+v", c)
	}
	h := histOf(t, c[1])
	if len(h) != 2 || h[1]["text"] != "A1" {
		t.Fatalf("second must see first's answer: %v", h)
	}
}

func TestDifferentContextsParallelButCapped(t *testing.T) {
	f := axbackend.NewFake()
	gate := make(chan struct{})
	f.OnGet = func(ctx context.Context, s axbackend.TaskSpec) axbackend.Status {
		<-gate
		return axbackend.Status{Done: true, Succeeded: true, Output: "ok"}
	}
	r := New(f, baseCfg()) // cap 2
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func() { defer wg.Done(); r.Run(context.Background(), id, "q") }()
	}
	waitFor(t, func() bool { return f.Active() == 2 })
	time.Sleep(80 * time.Millisecond)
	if f.Active() != 2 || len(f.Created()) != 2 {
		t.Fatalf("cap violated: active=%d created=%d", f.Active(), len(f.Created()))
	}
	close(gate)
	wg.Wait()
	if f.MaxActive() != 2 || len(f.Created()) != 3 {
		t.Fatalf("maxActive=%d created=%d", f.MaxActive(), len(f.Created()))
	}
}

func TestUsageSurfaced(t *testing.T) {
	f := axbackend.NewFake()
	f.OnGet = okOut(`{"type":"result","is_error":false,"result":"4 vulns","num_turns":3,"total_cost_usd":0.0123,
	 "usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000}}`)
	res := New(f, baseCfg()).Run(context.Background(), "c", "q")
	if res.Failed || res.Text != "4 vulns" {
		t.Fatalf("res: %+v", res)
	}
	u := res.Usage
	if u["inputTokens"] != 10 || u["outputTokens"] != 20 || u["cachedInputTokens"] != 1000 || u["totalTokens"] != 1130 {
		t.Fatalf("usage: %v", u)
	}
	if res.Claude["num_turns"] != float64(3) || res.Claude["total_cost_usd"] != 0.0123 {
		t.Fatalf("claude: %v", res.Claude)
	}
}

func TestPlainTextOutputIsAnswer(t *testing.T) {
	f := axbackend.NewFake()
	f.OnGet = okOut("just text {not json")
	res := New(f, baseCfg()).Run(context.Background(), "c", "q")
	if res.Failed || res.Text != "just text {not json" || res.Usage != nil {
		t.Fatalf("res: %+v", res)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
