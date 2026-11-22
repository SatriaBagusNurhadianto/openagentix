// Package embed turns text into vectors. The knowledge base works without an
// embedder (lexical search only); when one is configured, chunks and queries are
// embedded and search becomes hybrid.
package embed

import "context"

// Embedder converts texts to vectors. Implementations return one vector per
// input, in input order, each of length Dimensions().
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimensions is the vector length, or 0 if the embedder is disabled.
	Dimensions() int
}

// None is the disabled embedder: lexical search only.
type None struct{}

func (None) Embed(context.Context, []string) ([][]float32, error) { return nil, nil }
func (None) Dimensions() int                                      { return 0 }

// QueryEmbedder is optionally implemented by embedders that have a cheaper,
// single-attempt path for latency-sensitive query embedding (no retry).
type QueryEmbedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}
