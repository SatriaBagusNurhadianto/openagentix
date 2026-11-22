// Package bridge wires Telegram updates to an Agent.
package bridge

import "sync"

// ContextStore maps a Telegram chat id to an agent conversation context id.
type ContextStore interface {
	Get(chatID int64) string
	Set(chatID int64, contextID string)
	Delete(chatID int64)
}

type MemoryStore struct {
	mu sync.Mutex
	m  map[int64]string
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[int64]string{}} }

func (s *MemoryStore) Get(chatID int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[chatID]
}

func (s *MemoryStore) Set(chatID int64, contextID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[chatID] = contextID
}

func (s *MemoryStore) Delete(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, chatID)
}
