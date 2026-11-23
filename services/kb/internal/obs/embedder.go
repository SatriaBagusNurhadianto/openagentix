package obs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openagentix/openagentix/services/kb/internal/embed"
)

// WrapEmbedder counts and times every call of e. With metrics off, or for the
// disabled embedder, it returns e unchanged. The result also implements
// embed.QueryEmbedder, so the single-attempt query path keeps working.
func WrapEmbedder(e embed.Embedder, m *Metrics) embed.Embedder {
	if m == nil {
		return e
	}
	if _, none := e.(embed.None); none {
		return e
	}
	return &embedder{inner: e, m: m}
}

type embedder struct {
	inner embed.Embedder
	m     *Metrics
}

func (e *embedder) Dimensions() int { return e.inner.Dimensions() }

func embedOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "error"
}

func (e *embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	start := time.Now()
	v, err := e.inner.Embed(ctx, texts)
	e.m.EmbedCall("batch", embedOutcome(err), time.Since(start))
	return v, err
}

func (e *embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	start := time.Now()
	var v []float32
	var err error
	if qe, ok := e.inner.(embed.QueryEmbedder); ok {
		v, err = qe.EmbedQuery(ctx, text)
	} else {
		var vecs [][]float32
		if vecs, err = e.inner.Embed(ctx, []string{text}); err == nil {
			if len(vecs) != 1 {
				err = fmt.Errorf("embedder returned %d vectors for 1 query", len(vecs))
			} else {
				v = vecs[0]
			}
		}
	}
	e.m.EmbedCall("query", embedOutcome(err), time.Since(start))
	return v, err
}
