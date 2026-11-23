package graph

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/obs"
	"github.com/openagentix/openagentix/services/kb/internal/store"
)

// Extractor turns one document window into a validated Extraction.
type Extractor interface {
	Extract(ctx context.Context, w Window) (Extraction, Usage, error)
	// Model names the model; it is stored with every relation.
	Model() string
}

// Enqueue plans the extraction windows of a document and makes its jobs match
// them. Identical content (same windows, same prompt version) enqueues nothing
// and keeps finished work; anything else replaces the document's graph rows and
// jobs. It returns the number of jobs now waiting for a worker.
func Enqueue(ctx context.Context, st store.Store, collection, documentID string, chunks []string, windowChars int, promptVersion string) (int, error) {
	return st.SyncExtractionJobs(ctx, collection, documentID, promptVersion, Specs(Plan(chunks, windowChars)))
}

// Options tunes a Worker. Zero values select the defaults noted.
type Options struct {
	PromptVersion string        // stored with relations; required
	TokensPerHour int           // budget over a sliding hour; <= 0 means unlimited
	Lease         time.Duration // how long a claimed job is invisible to others; default 3m
	PollInterval  time.Duration // idle wait between polls; default 2s
	MaxAttempts   int           // attempts before a job is failed; default 5
	BackoffBase   time.Duration // delay after the first failed attempt, doubling; default 5s
	BackoffMax    time.Duration // default 10m
	Now           func() time.Time
	// Metrics, when non-nil, counts job results, model calls, tokens and budget
	// pauses. Only codes and counts: never document text, ids or collections.
	Metrics *obs.Metrics
}

func (o *Options) defaults() {
	if o.Lease <= 0 {
		o.Lease = 3 * time.Minute
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.BackoffBase <= 0 {
		o.BackoffBase = 5 * time.Second
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = 10 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Worker claims extraction jobs from the store and runs them one at a time. It
// is safe to run several; a job is leased to one at a time and a result is
// applied at most once.
type Worker struct {
	store     store.Store
	extractor Extractor
	embedder  embed.Embedder // optional
	log       *slog.Logger
	opts      Options
	budget    *budget
}

// NewWorker returns a worker. embedder may be nil (no entity name embeddings).
func NewWorker(st store.Store, ex Extractor, embedder embed.Embedder, log *slog.Logger, opts Options) *Worker {
	opts.defaults()
	if embedder != nil && embedder.Dimensions() == 0 {
		embedder = nil
	}
	return &Worker{store: st, extractor: ex, embedder: embedder, log: log, opts: opts, budget: &budget{limit: opts.TokensPerHour}}
}

// Run processes jobs until ctx is done, then returns ctx's error. A job that is
// in flight when ctx ends is left leased; another process takes it over when the
// lease expires.
func (w *Worker) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		worked, wait := w.Step(ctx)
		if worked {
			continue
		}
		budgetPause := wait > 0
		if !budgetPause {
			wait = w.opts.PollInterval
		} else {
			w.log.Info("extraction paused: hourly token budget used", "wait_s", int(wait.Seconds()), "tokens_per_hour", w.opts.TokensPerHour)
		}
		began := time.Now()
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		if budgetPause {
			w.opts.Metrics.GraphPaused(time.Since(began))
		}
	}
	return ctx.Err()
}

// Step runs at most one job. worked reports that a job was claimed (whatever its
// outcome). When nothing ran because the token budget is used up, wait says how
// long until it has room again.
func (w *Worker) Step(ctx context.Context) (worked bool, wait time.Duration) {
	if d := w.budget.wait(w.opts.Now()); d > 0 {
		return false, d
	}
	job, ok, err := w.store.ClaimJob(ctx, w.opts.Lease)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("claiming extraction job failed", "code", CodeStore)
		}
		return false, 0
	}
	if !ok {
		return false, 0
	}
	w.run(ctx, job)
	return true, 0
}

func (w *Worker) run(ctx context.Context, job store.Job) {
	start := time.Now()
	log := w.log.With("collection", job.Collection, "document_id", job.DocumentID, "window", job.WindowIdx, "attempt", job.Attempts)
	fail := func(code string, retryable bool, after time.Duration) {
		final := !retryable || job.Attempts >= w.opts.MaxAttempts
		retryIn := max(after, w.backoff(job.Attempts))
		if err := w.store.FailJob(ctx, job.JobKey, code, retryIn, final); err != nil && !errors.Is(err, store.ErrJobGone) && ctx.Err() == nil {
			log.Error("recording a failed extraction failed", "code", CodeStore)
		}
		if final {
			w.opts.Metrics.GraphJob("failed")
		} else {
			w.opts.Metrics.GraphJob("retry")
		}
		log.Warn("extraction failed", "code", code, "final", final, "retry_in_s", int(retryIn.Seconds()), "duration_ms", time.Since(start).Milliseconds())
	}

	chunks, err := w.store.DocumentChunks(ctx, job.Collection, job.DocumentID)
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, store.ErrNotFound):
		fail(CodeStaleWindow, false, 0)
		return
	case err != nil:
		fail(CodeStore, true, 0)
		return
	}
	win, ok := Rebuild(chunks, job.WindowIdx, job.FirstChunk, job.ChunkCount)
	if !ok || win.Hash != job.WindowHash {
		fail(CodeStaleWindow, false, 0) // the document changed after the job was planned
		return
	}

	ex, usage, err := w.extractor.Extract(ctx, win)
	w.budget.add(w.opts.Now(), usage.Tokens)
	w.opts.Metrics.GraphTokens(usage.Tokens)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down: leave the lease to expire
		}
		var ee *Error
		if errors.As(err, &ee) {
			w.opts.Metrics.GraphModelCall("error", ee.Code)
			fail(ee.Code, ee.Retryable, ee.RetryAfter)
		} else {
			w.opts.Metrics.GraphModelCall("error", CodeTransport)
			fail(CodeTransport, true, 0)
		}
		return
	}
	w.opts.Metrics.GraphModelCall("ok", "")

	w.embedNames(ctx, log, ex.Entities)
	err = w.store.ApplyExtraction(ctx, store.ExtractionResult{
		Job: job.JobKey, Model: w.extractor.Model(), PromptVersion: job.PromptVersion, Entities: ex.Entities, Relations: ex.Relations,
	})
	switch {
	case errors.Is(err, store.ErrJobGone):
		w.opts.Metrics.GraphJob("discarded")
		log.Info("extraction result discarded: the job is gone", "duration_ms", time.Since(start).Milliseconds())
		return
	case ctx.Err() != nil:
		return
	case err != nil:
		fail(CodeStore, true, 0)
		return
	}
	w.opts.Metrics.GraphJob("done")
	log.Info("extraction done", "entities", len(ex.Entities), "relations", len(ex.Relations),
		"dropped_entities", ex.Stats.EntitiesDropped, "dropped_relations", ex.Stats.RelationsDropped,
		"tokens", usage.Tokens, "duration_ms", time.Since(start).Milliseconds())
}

// embedNames fills in entity name embeddings. A failure is not fatal: the
// entities are stored without them and are still found by name.
func (w *Worker) embedNames(ctx context.Context, log *slog.Logger, es []store.ExtractedEntity) {
	if w.embedder == nil || len(es) == 0 {
		return
	}
	names := make([]string, len(es))
	for i, e := range es {
		names[i] = e.Name
	}
	vecs, err := w.embedder.Embed(ctx, names)
	if err != nil || len(vecs) != len(es) {
		if ctx.Err() == nil {
			log.Warn("embedding entity names failed; storing them without embeddings", "code", "embed")
		}
		return
	}
	for i := range es {
		es[i].NameEmbedding = vecs[i]
	}
}

// backoff is the delay before attempt n+1 after n failed attempts.
func (w *Worker) backoff(attempts int) time.Duration {
	d := w.opts.BackoffBase
	for i := 1; i < attempts && d < w.opts.BackoffMax; i++ {
		d *= 2
	}
	return min(d, w.opts.BackoffMax)
}

// budget counts tokens used over a sliding hour.
type budget struct {
	limit   int
	mu      sync.Mutex
	entries []usageEntry
}

type usageEntry struct {
	at time.Time
	n  int
}

func (b *budget) add(now time.Time, n int) {
	if b.limit <= 0 || n <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = append(b.entries, usageEntry{now, n})
}

// wait returns how long until the budget has room again, 0 if it has now.
func (b *budget) wait(now time.Time) time.Duration {
	if b.limit <= 0 {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	keep := b.entries[:0]
	sum := 0
	for _, e := range b.entries {
		if now.Sub(e.at) < time.Hour {
			keep = append(keep, e)
			sum += e.n
		}
	}
	b.entries = keep
	if sum < b.limit {
		return 0
	}
	for _, e := range b.entries { // oldest first: wait until enough of them expire
		sum -= e.n
		if sum < b.limit {
			return e.at.Add(time.Hour).Sub(now) + time.Millisecond
		}
	}
	return time.Hour
}
