// Package turn runs one conversational turn as one AX Task: create the task
// with the user text as goal and the bounded history in spec.config, resume
// it, poll until it ends, map the outcome, and delete the task.
package turn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/axbackend"
	"github.com/openagentix/openagentix/bridges/ax-adapter/internal/history"
)

type Config struct {
	Atespace, Workspace, Gateway, Image string
	TurnTimeout, PollInterval           time.Duration
	KeepTasks                           bool
	MaxHistoryTurns                     int
	ConfigBudgetBytes                   int // max JSON size of spec.config (AX hard limit is 16 KiB)
	MaxMessageBytes                     int
	MaxConcurrentTasks                  int
	MaxContexts                         int
}

type Result struct {
	ContextID, TaskID string
	Text              string // the answer, or the failure message when Failed
	Failed            bool
	Usage             map[string]any // kagent-style usage (see parseOutput)
	Claude            map[string]any // total_cost_usd / num_turns / cache_creation passthrough
}

type Runner struct {
	b     axbackend.Backend
	cfg   Config
	store *history.Store
	sem   chan struct{}
}

func New(b axbackend.Backend, cfg Config) *Runner {
	if cfg.MaxConcurrentTasks < 1 {
		cfg.MaxConcurrentTasks = 1
	}
	if cfg.MaxContexts < 1 {
		cfg.MaxContexts = 1000
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	return &Runner{b: b, cfg: cfg, store: history.NewStore(cfg.MaxHistoryTurns, cfg.MaxContexts),
		sem: make(chan struct{}, cfg.MaxConcurrentTasks)}
}

const maxConsecutivePollErrors = 5

// Run never returns an error: every failure is a Result with Failed set, so
// the A2A layer can answer HTTP 200 + TASK_STATE_FAILED like kagent does.
func (r *Runner) Run(ctx context.Context, contextID, text string) Result {
	if contextID == "" {
		contextID = newUUID()
	}
	res := Result{ContextID: contextID}
	fail := func(format string, a ...any) Result {
		res.Failed, res.Text = true, fmt.Sprintf(format, a...)
		return res
	}
	if r.cfg.MaxMessageBytes > 0 && len(text) > r.cfg.MaxMessageBytes {
		return fail("message too large: %d bytes (limit %d)", len(text), r.cfg.MaxMessageBytes)
	}

	ctx, cancel := context.WithTimeout(ctx, r.cfg.TurnTimeout)
	defer cancel()
	timedOut := func() Result {
		return fail("agent turn timed out after %s", r.cfg.TurnTimeout)
	}

	unlock, err := r.store.Lock(ctx, contextID)
	if err != nil {
		return timedOut()
	}
	defer unlock()

	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return timedOut()
	}

	spec := axbackend.TaskSpec{
		Name: "turn-" + randHex(5), Atespace: r.cfg.Atespace, Workspace: r.cfg.Workspace,
		Gateway: r.cfg.Gateway, Image: r.cfg.Image, Goal: text, SessionID: contextID,
		Config: buildConfig(contextID, r.store.Turns(contextID), r.cfg.ConfigBudgetBytes),
	}
	res.TaskID = spec.Name

	if err := r.b.CreateTask(ctx, spec); err != nil {
		return fail("could not create AX task: %v", err)
	}
	if !r.cfg.KeepTasks {
		defer func() {
			cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer ccancel()
			_ = r.b.DeleteTask(cctx, spec.Atespace, spec.Name)
		}()
	}
	if err := r.b.ResumeTask(ctx, spec.Atespace, spec.Name); err != nil {
		return fail("could not start AX task: %v", err)
	}

	st, ok := r.poll(ctx, spec)
	if !ok {
		return timedOut()
	}
	if !st.Succeeded {
		return fail("%s", describeFailure(st))
	}
	p := parseOutput(st.Output)
	if p.isError {
		return fail("agent reported an error: %s", truncate(p.text, 500))
	}
	if strings.TrimSpace(p.text) == "" {
		return fail("agent finished without producing an answer")
	}
	res.Text, res.Usage, res.Claude = p.text, p.usage, p.claude
	r.store.Append(contextID, history.Turn{User: text, Assistant: p.text})
	return res
}

func (r *Runner) poll(ctx context.Context, spec axbackend.TaskSpec) (axbackend.Status, bool) {
	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	errs := 0
	for {
		st, err := r.b.GetTask(ctx, spec.Atespace, spec.Name)
		switch {
		case ctx.Err() != nil:
			return st, false
		case err != nil:
			errs++
			if errs >= maxConsecutivePollErrors {
				return axbackend.Status{Done: true, Detail: "lost contact with AX: " + err.Error()}, true
			}
		case st.Done:
			return st, true
		default:
			errs = 0
		}
		select {
		case <-ctx.Done():
			return st, false
		case <-t.C:
		}
	}
}

func describeFailure(st axbackend.Status) string {
	msg := "agent task failed"
	if st.ExitCode != 0 {
		msg += fmt.Sprintf(" (exit code %d)", st.ExitCode)
	}
	if st.Detail != "" {
		msg += ": " + truncate(st.Detail, 300)
	}
	if st.Output != "" {
		msg += " | output tail: " + tail(st.Output, 500)
	}
	return msg
}

// buildConfig returns spec.config: contextId plus the newest history pairs
// whose JSON fits budget. Oldest pairs are dropped first, never half a pair.
func buildConfig(contextID string, turns []history.Turn, budget int) map[string]any {
	for start := 0; ; start++ {
		var h []any
		for _, t := range turns[start:] {
			h = append(h,
				map[string]any{"role": "user", "text": t.User},
				map[string]any{"role": "assistant", "text": t.Assistant})
		}
		cfg := map[string]any{"contextId": contextID, "history": h}
		if h == nil {
			cfg["history"] = []any{}
		}
		b, _ := json.Marshal(cfg)
		if budget <= 0 || len(b) <= budget || start >= len(turns) {
			return cfg
		}
	}
}

type parsed struct {
	text    string
	isError bool
	usage   map[string]any
	claude  map[string]any
}

// parseOutput understands Claude Code's `--output-format json` result object.
// Anything else is taken as the plain-text answer.
//
// usage mapping (document in README): inputTokens = input_tokens;
// cachedInputTokens = cache_read_input_tokens; outputTokens = output_tokens;
// totalTokens = input + cache_creation + cache_read + output (everything the
// model processed or produced).
func parseOutput(out string) parsed {
	var o map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &o); err != nil {
		return parsed{text: out}
	}
	res, ok := o["result"].(string)
	if !ok {
		return parsed{text: out}
	}
	p := parsed{text: res}
	p.isError, _ = o["is_error"].(bool)
	if u, ok := o["usage"].(map[string]any); ok {
		in, outT := num(u["input_tokens"]), num(u["output_tokens"])
		cc, cr := num(u["cache_creation_input_tokens"]), num(u["cache_read_input_tokens"])
		p.usage = map[string]any{"inputTokens": in, "outputTokens": outT, "cachedInputTokens": cr, "totalTokens": in + outT + cc + cr}
		p.claude = map[string]any{"cache_creation_input_tokens": cc}
	}
	for _, k := range []string{"total_cost_usd", "num_turns"} {
		if v, ok := o[k]; ok {
			if p.claude == nil {
				p.claude = map[string]any{}
			}
			p.claude[k] = v
		}
	}
	return p
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
