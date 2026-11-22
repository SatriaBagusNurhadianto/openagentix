// Package store defines the storage interface the knowledge base is built on.
// Backends (in-memory, PostgreSQL, ...) implement Store and are interchangeable.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotFound is returned when a document does not exist.
	ErrNotFound = errors.New("document not found")
	// ErrAmbiguous is returned when a document id without a collection matches
	// documents in more than one collection.
	ErrAmbiguous = errors.New("document id exists in more than one collection")
)

// Document is a stored document. Text is the full original text.
type Document struct {
	Collection string
	ID         string
	Title      string
	SourceURI  string
	Text       string
	UpdatedAt  time.Time
}

// Chunk is one retrievable piece of a document. Embedding is optional and, when
// present, must have the dimension the store was configured with.
type Chunk struct {
	Text      string
	Embedding []float32
}

// CollectionInfo summarises one collection.
type CollectionInfo struct {
	Name      string
	Documents int
}

// Query is a search request. Embedding is optional: with it (and a store that
// supports vectors) results are hybrid-ranked, without it lexical only.
type Query struct {
	Text       string
	Embedding  []float32
	Collection string // empty = all collections
	TopK       int
}

// Hit is one ranked chunk. Higher Score is better; scores are only comparable
// within one result list.
type Hit struct {
	Collection string
	DocumentID string
	Title      string
	SourceURI  string
	ChunkIndex int
	Text       string
	Score      float64
}

// Store is the persistence and retrieval backend.
type Store interface {
	// UpsertDocument atomically replaces the document and all its chunks.
	UpsertDocument(ctx context.Context, doc Document, chunks []Chunk) error
	// DeleteDocument removes a document; it reports whether it existed.
	DeleteDocument(ctx context.Context, collection, id string) (bool, error)
	// GetDocument returns a document. An empty collection matches any collection
	// but yields ErrAmbiguous if the id is not unique.
	GetDocument(ctx context.Context, collection, id string) (Document, error)
	// ListCollections returns collections sorted by name.
	ListCollections(ctx context.Context) ([]CollectionInfo, error)
	// Search returns at most q.TopK hits, best first.
	Search(ctx context.Context, q Query) ([]Hit, error)
	// Ping checks that the backend is reachable.
	Ping(ctx context.Context) error
	// Close releases resources.
	Close()
}
