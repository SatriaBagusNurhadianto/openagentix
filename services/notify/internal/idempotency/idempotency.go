// Package idempotency remembers recent results by key so a repeated request is
// answered without acting twice. It is in-process and bounded; with several
// replicas each has its own memory.
package idempotency

import (
	"sync"
	"time"
)

// Outcome is what Begin found.
type Outcome int

const (
	// Miss: the key is new and now reserved; the caller must Commit or Abort.
	Miss Outcome = iota
	// Hit: an earlier request with this key and content finished; use the value.
	Hit
	// InFlight: an earlier request with this key has not finished.
	InFlight
	// Mismatch: the key was used before with different content.
	Mismatch
	// Full: every slot is held by an unfinished request.
	Full
)

type entry[V any] struct {
	fingerprint string
	value       V
	done        bool
	at          time.Time
}

// Cache is a bounded, expiring key to result map.
type Cache[V any] struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	now     func() time.Time
	entries map[string]*entry[V]
}

// New returns a cache holding at most max entries for window each.
func New[V any](window time.Duration, max int, now func() time.Time) *Cache[V] {
	if now == nil {
		now = time.Now
	}
	return &Cache[V]{window: window, max: max, now: now, entries: map[string]*entry[V]{}}
}

// Len is the number of live entries.
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Begin looks key up. fingerprint identifies the request content.
func (c *Cache[V]) Begin(key, fingerprint string) (Outcome, V) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if e, ok := c.entries[key]; ok {
		if e.done && now.Sub(e.at) > c.window {
			delete(c.entries, key)
		} else {
			switch {
			case e.fingerprint != fingerprint:
				return Mismatch, zero
			case !e.done:
				return InFlight, zero
			default:
				return Hit, e.value
			}
		}
	}
	c.evict(now)
	if len(c.entries) >= c.max {
		return Full, zero
	}
	c.entries[key] = &entry[V]{fingerprint: fingerprint, at: now}
	return Miss, zero
}

// evict drops expired entries, then the oldest finished ones until there is room.
func (c *Cache[V]) evict(now time.Time) {
	for k, e := range c.entries {
		if e.done && now.Sub(e.at) > c.window {
			delete(c.entries, k)
		}
	}
	for len(c.entries) >= c.max {
		oldestKey, found := "", false
		var oldest time.Time
		for k, e := range c.entries {
			if e.done && (!found || e.at.Before(oldest)) {
				oldestKey, oldest, found = k, e.at, true
			}
		}
		if !found {
			return
		}
		delete(c.entries, oldestKey)
	}
}

// Commit stores the result of a Begin that returned Miss.
func (c *Cache[V]) Commit(key string, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.value, e.done, e.at = v, true, c.now()
	}
}

// Abort releases a reservation after a failed attempt so the key can be retried.
func (c *Cache[V]) Abort(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && !e.done {
		delete(c.entries, key)
	}
}
