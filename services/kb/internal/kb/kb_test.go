package kb

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openagentix/openagentix/services/kb/internal/chunk"
	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/store"
	"github.com/openagentix/openagentix/services/kb/internal/store/memory"
)

type fakeEmbedder struct {
	dim   int
	err   error
	calls [][]string
}

func (f *fakeEmbedder) Dimensions() int { return f.dim }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls = append(f.calls, texts)
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, s := range texts {
		v := make([]float32, f.dim)
		if strings.Contains(s, "apple") {
			v[0] = 1
		} else {
			v[1] = 1
		}
		out[i] = v
	}
	return out, nil
}

func newSvc(e embed.Embedder) *Service {
	return &Service{
		Store: memory.New(e.Dimensions()), Embedder: e,
		Chunk: chunk.Options{MaxChars: 60, Overlap: 0}, DocMaxChars: 50,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestPutThenSearchLexical(t *testing.T) {
	s := newSvc(embed.None{})
	ctx := context.Background()
	n, err := s.Put(ctx, "docs", "d1", DocInput{Title: "Fruit", SourceURI: "https://x.example/doc", Text: "Apples are red.\n\nBananas are yellow and long and curved like a smile."})
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	hits, err := s.Search(ctx, "bananas", "", 0)
	if err != nil || len(hits) != 1 || hits[0].DocumentID != "d1" || hits[0].ChunkIndex != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	s := newSvc(embed.None{})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Put(ctx, "docs", "d1", DocInput{Title: "T", Text: "same text"}); err != nil {
			t.Fatal(err)
		}
	}
	cols, _ := s.Collections(ctx)
	if len(cols) != 1 || cols[0].Documents != 1 {
		t.Fatalf("%+v", cols)
	}
}

func TestPutValidation(t *testing.T) {
	s := newSvc(embed.None{})
	ctx := context.Background()
	long := strings.Repeat("a", 129)
	cases := map[string]struct {
		coll, id string
		in       DocInput
	}{
		"empty collection": {"", "id", DocInput{Text: "x"}},
		"bad collection":   {"a/b", "id", DocInput{Text: "x"}},
		"long collection":  {long, "id", DocInput{Text: "x"}},
		"leading dot":      {".hidden", "id", DocInput{Text: "x"}},
		"empty id":         {"c", "", DocInput{Text: "x"}},
		"id with space":    {"c", "a b", DocInput{Text: "x"}},
		"id with slash":    {"c", "../etc", DocInput{Text: "x"}},
		"long id":          {"c", strings.Repeat("a", 201), DocInput{Text: "x"}},
		"empty text":       {"c", "id", DocInput{Text: "  \n "}},
		"invalid utf8":     {"c", "id", DocInput{Text: "ab\xff"}},
		"NUL in text":      {"c", "id", DocInput{Text: "a\x00b"}},
		"long title":       {"c", "id", DocInput{Title: strings.Repeat("t", 501), Text: "x"}},
		"long source":      {"c", "id", DocInput{SourceURI: strings.Repeat("u", 2049), Text: "x"}},
		"NUL in title":     {"c", "id", DocInput{Title: "a\x00", Text: "x"}},
		"javascript uri":   {"c", "id", DocInput{SourceURI: "javascript:alert(1)", Text: "x"}},
		"file uri":         {"c", "id", DocInput{SourceURI: "file:///etc/passwd", Text: "x"}},
		"scheme-less uri":  {"c", "id", DocInput{SourceURI: "example.com/x", Text: "x"}},
		"data uri":         {"c", "id", DocInput{SourceURI: "data:text/html,hi", Text: "x"}},
		"host-less http":   {"c", "id", DocInput{SourceURI: "http://", Text: "x"}},
	}
	for name, c := range cases {
		_, err := s.Put(ctx, c.coll, c.id, c.in)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.Put(ctx, "Team_A.v1-x", "doc:2024.report-1", DocInput{Text: "ok"}); err != nil {
		t.Fatalf("valid names rejected: %v", err)
	}
	for _, u := range []string{"", "http://wiki.example/x", "HTTPS://wiki.example/x?a=b#c"} {
		if _, err := s.Put(ctx, "c", "uri", DocInput{SourceURI: u, Text: "ok"}); err != nil {
			t.Errorf("source_uri %q rejected: %v", u, err)
		}
	}
}

func TestPutEmbedsChunksInOneCall(t *testing.T) {
	e := &fakeEmbedder{dim: 3}
	s := newSvc(e)
	ctx := context.Background()
	if _, err := s.Put(ctx, "c", "a", DocInput{Text: "apple pie.\n\nbanana split is a long dessert indeed yes, very much so."}); err != nil {
		t.Fatal(err)
	}
	if len(e.calls) != 1 || len(e.calls[0]) != 2 {
		t.Fatalf("calls = %v", e.calls)
	}
}

func TestPutEmbedderFailureIsUpstreamErrorAndStoresNothing(t *testing.T) {
	e := &fakeEmbedder{dim: 3, err: errors.New("boom")}
	s := newSvc(e)
	_, err := s.Put(context.Background(), "c", "a", DocInput{Text: "x"})
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v", err)
	}
	if cols, _ := s.Collections(context.Background()); len(cols) != 0 {
		t.Fatalf("stored despite failure: %+v", cols)
	}
}

func TestSearchHybridUsesQueryEmbedding(t *testing.T) {
	e := &fakeEmbedder{dim: 3}
	s := newSvc(e)
	ctx := context.Background()
	_, _ = s.Put(ctx, "c", "fruit", DocInput{Text: "apple orchard"})
	_, _ = s.Put(ctx, "c", "other", DocInput{Text: "zebra crossing"})
	hits, err := s.Search(ctx, "apple", "c", 5)
	if err != nil || len(hits) == 0 || hits[0].DocumentID != "fruit" {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	if e.calls[len(e.calls)-1][0] != "apple" {
		t.Fatalf("query not embedded: %v", e.calls)
	}
}

func TestSearchDegradesToLexicalWhenEmbedderFails(t *testing.T) {
	e := &fakeEmbedder{dim: 3}
	s := newSvc(e)
	ctx := context.Background()
	_, _ = s.Put(ctx, "c", "a", DocInput{Text: "needle haystack"})
	e.err = errors.New("embedder down")
	hits, err := s.Search(ctx, "needle", "", 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

func TestSearchTopKBounds(t *testing.T) {
	s := newSvc(embed.None{})
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		_, _ = s.Put(ctx, "c", "d"+string(rune('a'+i%26))+string(rune('a'+i/26)), DocInput{Text: "common term"})
	}
	for _, tc := range []struct{ in, want int }{{0, 5}, {-3, 5}, {2, 2}, {20, 20}, {500, 20}} {
		hits, err := s.Search(ctx, "common", "", tc.in)
		if err != nil || len(hits) != tc.want {
			t.Errorf("topK=%d: got %d hits, err=%v, want %d", tc.in, len(hits), err, tc.want)
		}
	}
}

func TestSearchValidatesInput(t *testing.T) {
	s := newSvc(embed.None{})
	for _, tc := range []struct{ q, c string }{{"", ""}, {"   ", ""}, {"ok", "bad/coll"}, {strings.Repeat("q", 2001), ""}} {
		if _, err := s.Search(context.Background(), tc.q, tc.c, 5); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: err = %v", tc, err)
		}
	}
}

func TestGetTruncatesWithMarker(t *testing.T) {
	s := newSvc(embed.None{})
	ctx := context.Background()
	text := strings.Repeat("héllo ", 30) // 180 runes
	_, _ = s.Put(ctx, "c", "big", DocInput{Title: "Big", Text: text})
	_, _ = s.Put(ctx, "c", "small", DocInput{Title: "Small", Text: "tiny"})

	v, err := s.Get(ctx, "c", "big")
	if err != nil || !v.Truncated || v.TotalChars != 180 {
		t.Fatalf("%+v %v", v, err)
	}
	if !strings.Contains(v.Text, "[truncated:") || !utf8.ValidString(v.Text) {
		t.Fatalf("text = %q", v.Text)
	}
	if !strings.HasPrefix(v.Text, "héllo héllo") {
		t.Fatalf("prefix lost: %q", v.Text)
	}
	v, _ = s.Get(ctx, "c", "small")
	if v.Truncated || v.Text != "tiny" {
		t.Fatalf("%+v", v)
	}
}

func TestGetNotFoundAndAmbiguous(t *testing.T) {
	s := newSvc(embed.None{})
	ctx := context.Background()
	_, _ = s.Put(ctx, "a", "dup", DocInput{Text: "x"})
	_, _ = s.Put(ctx, "b", "dup", DocInput{Text: "x"})
	if _, err := s.Get(ctx, "", "dup"); !errors.Is(err, store.ErrAmbiguous) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Get(ctx, "a", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Get(ctx, "a", "bad id"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := newSvc(embed.None{})
	ctx := context.Background()
	_, _ = s.Put(ctx, "c", "a", DocInput{Text: "x"})
	ok, err := s.Delete(ctx, "c", "a")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if ok, _ = s.Delete(ctx, "c", "a"); ok {
		t.Fatal("second delete should report absent")
	}
	if _, err := s.Delete(ctx, "bad/c", "a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestTruncateToHandlesDefaults(t *testing.T) {
	s := newSvc(embed.None{})
	s.DocMaxChars = 0 // unset => default
	_, _ = s.Put(context.Background(), "c", "a", DocInput{Text: "short"})
	v, err := s.Get(context.Background(), "c", "a")
	if err != nil || v.Truncated {
		t.Fatalf("%+v %v", v, err)
	}
}

// blockingEmbedder blocks until its context is cancelled.
type blockingEmbedder struct{ dim int }

func (b blockingEmbedder) Dimensions() int { return b.dim }
func (b blockingEmbedder) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSearchBoundsQueryEmbeddingAndFallsBack(t *testing.T) {
	s := newSvc(embed.None{})
	if _, err := s.Put(context.Background(), "docs", "d1", DocInput{Text: "Bananas are yellow."}); err != nil {
		t.Fatal(err)
	}
	s.Embedder = blockingEmbedder{dim: 3}
	s.QueryEmbedTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	hits, err := s.Search(ctx, "bananas", "", 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
	if ctx.Err() != nil {
		t.Fatal("request context was consumed")
	}
}

func TestPutRejectsTooManyChunksBeforeEmbedding(t *testing.T) {
	e := &fakeEmbedder{dim: 3}
	s := newSvc(e)
	s.MaxChunksPerDoc = 3
	text := strings.Repeat("A sentence of some length goes here.\n\n", 10)
	_, err := s.Put(context.Background(), "docs", "big", DocInput{Text: text})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if len(e.calls) != 0 {
		t.Fatalf("embedder was called %d times before the cap check", len(e.calls))
	}
	if cols, _ := s.Collections(context.Background()); len(cols) != 0 {
		t.Fatalf("partial write: %+v", cols)
	}
	// At the limit it is accepted.
	s.MaxChunksPerDoc = 100
	if _, err := s.Put(context.Background(), "docs", "big", DocInput{Text: text}); err != nil {
		t.Fatal(err)
	}
}

func TestPutContextExpiryDuringEmbeddingIsNotUpstreamError(t *testing.T) {
	s := newSvc(blockingEmbedder{dim: 3})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.Put(ctx, "c", "d", DocInput{Text: "hello"})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want the context error (not ErrUpstream)", err)
	}
}

func graphSvc() *Service {
	s := newSvc(embed.None{})
	s.Graph = &GraphOptions{WindowChars: 100, PromptVersion: "v1"}
	return s
}

func pendingJobs(t *testing.T, s *Service, coll string) store.GraphStats {
	t.Helper()
	st, err := s.Store.GraphStats(context.Background(), coll)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestPutEnqueuesExtractionOnlyWhenGraphIsOn(t *testing.T) {
	ctx := context.Background()
	text := "Alpha service calls Beta service.\n\nBeta service is owned by the Core team and runs in Frankfurt."
	off := newSvc(embed.None{})
	if _, err := off.Put(ctx, "docs", "d", DocInput{Text: text}); err != nil {
		t.Fatal(err)
	}
	if st := pendingJobs(t, off, "docs"); st.JobsPending != 0 {
		t.Fatalf("graph off but jobs queued: %+v", st)
	}
	on := graphSvc()
	if _, err := on.Put(ctx, "docs", "d", DocInput{Text: text}); err != nil {
		t.Fatal(err)
	}
	if st := pendingJobs(t, on, "docs"); st.JobsPending == 0 {
		t.Fatalf("graph on but nothing queued: %+v", st)
	}
}

func TestPutOfIdenticalContentKeepsFinishedExtractionAndChangedContentResets(t *testing.T) {
	ctx := context.Background()
	s := graphSvc()
	text := "Alpha service calls Beta service."
	if _, err := s.Put(ctx, "docs", "d", DocInput{Text: text}); err != nil {
		t.Fatal(err)
	}
	job, ok, _ := s.Store.ClaimJob(ctx, time.Minute)
	if !ok {
		t.Fatal("no job")
	}
	if err := s.Store.ApplyExtraction(ctx, store.ExtractionResult{Job: job.JobKey, PromptVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "docs", "d", DocInput{Title: "retitled", Text: text}); err != nil {
		t.Fatal(err)
	}
	if st := pendingJobs(t, s, "docs"); st.JobsDone != 1 || st.JobsPending != 0 {
		t.Fatalf("identical re-put redid the work: %+v", st)
	}
	if _, err := s.Put(ctx, "docs", "d", DocInput{Text: text + " Gamma service too."}); err != nil {
		t.Fatal(err)
	}
	if st := pendingJobs(t, s, "docs"); st.JobsDone != 0 || st.JobsPending != 1 {
		t.Fatalf("changed re-put did not queue a new extraction: %+v", st)
	}
}
