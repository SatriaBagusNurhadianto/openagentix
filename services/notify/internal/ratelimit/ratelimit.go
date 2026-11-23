// Package ratelimit is a small two-window token bucket (per minute and per
// hour). It is in-process: with several replicas each one has its own budget.
package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	capacity float64
	per      time.Duration // time to refill the whole bucket
	tokens   float64
}

func (b *bucket) refill(elapsed time.Duration) {
	b.tokens += b.capacity * float64(elapsed) / float64(b.per)
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

// wait is how long until n tokens are available (0 if they are). n is
// clamped to the capacity, since more could never be available.
func (b *bucket) wait(n float64) time.Duration {
	n = min(n, b.capacity)
	if b.tokens >= n {
		return 0
	}
	need := n - b.tokens
	return time.Duration(need / b.capacity * float64(b.per))
}

// Limiter admits calls while both windows have a token.
type Limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	last    time.Time
	buckets []*bucket
}

// New builds a limiter allowing perMinute calls per minute and perHour per
// hour, each as a token bucket with a burst equal to its size. Zero disables
// that window. now may be nil.
func New(perMinute, perHour int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	l := &Limiter{now: now, last: now()}
	if perMinute > 0 {
		l.buckets = append(l.buckets, &bucket{capacity: float64(perMinute), per: time.Minute, tokens: float64(perMinute)})
	}
	if perHour > 0 {
		l.buckets = append(l.buckets, &bucket{capacity: float64(perHour), per: time.Hour, tokens: float64(perHour)})
	}
	return l
}

// Allow consumes one token from each window, or consumes nothing and returns
// how long to wait before the next call can succeed.
func (l *Limiter) Allow() (bool, time.Duration) { return l.AllowN(1) }

// AllowN consumes n tokens from each window, all or nothing. A request for
// more than a window's capacity is treated as a request for the whole
// capacity, so it is admitted by a full bucket rather than refused forever.
func (l *Limiter) AllowN(n int) (bool, time.Duration) {
	if n < 1 {
		n = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	elapsed := t.Sub(l.last)
	l.last = t
	var retry time.Duration
	for _, b := range l.buckets {
		if elapsed > 0 {
			b.refill(elapsed)
		}
		retry = max(retry, b.wait(float64(n)))
	}
	if retry > 0 {
		return false, retry.Round(time.Millisecond)
	}
	for _, b := range l.buckets {
		b.tokens -= min(float64(n), b.capacity)
	}
	return true, 0
}

// Refund gives n tokens back to every window (up to capacity), for a call that
// consumed them here but was then refused elsewhere.
func (l *Limiter) Refund(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.buckets {
		b.tokens = min(b.capacity, b.tokens+float64(n))
	}
}
