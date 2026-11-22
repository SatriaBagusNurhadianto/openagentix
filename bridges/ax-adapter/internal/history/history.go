// Package history keeps per-context conversation history in memory, bounded
// by turns per context and by number of contexts (LRU), and provides a
// context-aware per-conversation lock.
package history

import (
	"context"
	"sync"
)

// Turn is one successful user/assistant exchange.
type Turn struct{ User, Assistant string }

type entry struct {
	turns []Turn
	lock  chan struct{} // capacity 1; holding a token = holding the conversation
	refs  int           // goroutines holding or waiting on lock
	seq   uint64        // last-use sequence for LRU
}

// Store is safe for concurrent use.
type Store struct {
	mu          sync.Mutex
	maxTurns    int
	maxContexts int
	seq         uint64
	m           map[string]*entry
}

func NewStore(maxTurns, maxContexts int) *Store {
	return &Store{maxTurns: maxTurns, maxContexts: maxContexts, m: map[string]*entry{}}
}

// get returns the entry, creating it and evicting LRU unlocked entries. Caller holds s.mu.
func (s *Store) get(id string) *entry {
	s.seq++
	if e, ok := s.m[id]; ok {
		e.seq = s.seq
		return e
	}
	for len(s.m) >= s.maxContexts && s.maxContexts > 0 {
		var victim string
		var best uint64
		found := false
		for k, e := range s.m {
			if e.refs == 0 && (!found || e.seq < best) {
				victim, best, found = k, e.seq, true
			}
		}
		if !found {
			break
		}
		delete(s.m, victim)
	}
	e := &entry{lock: make(chan struct{}, 1), seq: s.seq}
	s.m[id] = e
	return e
}

// Turns returns a copy of the stored turns (oldest first).
func (s *Store) Turns(id string) []Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; !ok {
		return nil
	}
	e := s.get(id)
	return append([]Turn(nil), e.turns...)
}

// Append records a successful turn and drops the oldest beyond maxTurns.
func (s *Store) Append(id string, t Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.get(id)
	e.turns = append(e.turns, t)
	if s.maxTurns > 0 && len(e.turns) > s.maxTurns {
		e.turns = append([]Turn(nil), e.turns[len(e.turns)-s.maxTurns:]...)
	}
}

// Lock serializes turns of one conversation. It returns when the lock is
// held or ctx is done.
func (s *Store) Lock(ctx context.Context, id string) (func(), error) {
	s.mu.Lock()
	e := s.get(id)
	e.refs++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		e.refs--
		s.mu.Unlock()
	}
	select {
	case e.lock <- struct{}{}:
		return func() { <-e.lock; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
