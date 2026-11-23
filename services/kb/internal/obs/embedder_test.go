package obs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/kb/internal/embed"
)

type stubEmbedder struct {
	err   error
	query bool
}

func (s *stubEmbedder) Dimensions() int { return 2 }
func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 2}
	}
	return out, nil
}

type queryStub struct{ stubEmbedder }

func (q *queryStub) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	q.query = true
	if q.err != nil {
		return nil, q.err
	}
	return []float32{1, 2}, nil
}

func TestWrapEmbedderCountsBatchAndQueryCalls(t *testing.T) {
	m := New("dev")
	inner := &queryStub{}
	e := WrapEmbedder(inner, m)
	if e.Dimensions() != 2 {
		t.Fatal("dimensions not passed through")
	}
	if _, err := e.Embed(context.Background(), []string{"secret document text"}); err != nil {
		t.Fatal(err)
	}
	qe, ok := e.(embed.QueryEmbedder)
	if !ok {
		t.Fatal("wrapper must keep the QueryEmbedder fast path")
	}
	if _, err := qe.EmbedQuery(context.Background(), "secret query"); err != nil || !inner.query {
		t.Fatalf("query path not used: %v", err)
	}
	inner.err = context.DeadlineExceeded
	_, _ = qe.EmbedQuery(context.Background(), "q")
	inner.err = errors.New("boom: secret")
	_, _ = e.Embed(context.Background(), []string{"x"})
	out := scrape(t, m)
	for _, want := range []string{
		`openagentix_kb_embedder_calls_total{op="batch",outcome="ok"} 1`,
		`openagentix_kb_embedder_calls_total{op="query",outcome="ok"} 1`,
		`openagentix_kb_embedder_calls_total{op="query",outcome="timeout"} 1`,
		`openagentix_kb_embedder_calls_total{op="batch",outcome="error"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret") {
		t.Error("text or error leaked into metrics")
	}
}

func TestWrapEmbedderWithoutQueryPathUsesEmbed(t *testing.T) {
	m := New("dev")
	e := WrapEmbedder(&stubEmbedder{}, m).(embed.QueryEmbedder)
	v, err := e.EmbedQuery(context.Background(), "q")
	if err != nil || len(v) != 2 {
		t.Fatalf("%v %v", v, err)
	}
	if !strings.Contains(scrape(t, m), `op="query",outcome="ok"} 1`) {
		t.Fatal("query call not counted")
	}
}

func TestWrapEmbedderNilMetricsReturnsInner(t *testing.T) {
	inner := &stubEmbedder{}
	if WrapEmbedder(inner, nil) != embed.Embedder(inner) {
		t.Fatal("with metrics off the embedder must be returned unchanged")
	}
	if _, ok := WrapEmbedder(embed.None{}, New("dev")).(embed.None); !ok {
		t.Fatal("the disabled embedder must stay the disabled embedder (Dimensions 0 gates search)")
	}
}
