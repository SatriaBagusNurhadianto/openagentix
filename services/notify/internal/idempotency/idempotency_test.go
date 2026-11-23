package idempotency

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newCache(max int) (*Cache[string], *clock) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return New[string](10*time.Minute, max, c.now), c
}

func TestBegin_FirstUseIsMissThenCommittedKeyIsHit(t *testing.T) {
	c, _ := newCache(10)
	if o, _ := c.Begin("k", "fp"); o != Miss {
		t.Fatalf("first = %v", o)
	}
	c.Commit("k", "result-1")
	o, v := c.Begin("k", "fp")
	if o != Hit || v != "result-1" {
		t.Fatalf("second = %v %q", o, v)
	}
}

func TestBegin_SameKeyWhileFirstInFlight(t *testing.T) {
	c, _ := newCache(10)
	c.Begin("k", "fp")
	if o, _ := c.Begin("k", "fp"); o != InFlight {
		t.Fatalf("got %v, want InFlight", o)
	}
}

func TestBegin_SameKeyDifferentContentIsMismatch(t *testing.T) {
	c, _ := newCache(10)
	c.Begin("k", "fp1")
	c.Commit("k", "r")
	if o, _ := c.Begin("k", "fp2"); o != Mismatch {
		t.Fatalf("got %v, want Mismatch", o)
	}
}

func TestAbort_FailedSendCanBeRetriedWithSameKey(t *testing.T) {
	c, _ := newCache(10)
	c.Begin("k", "fp")
	c.Abort("k")
	if o, _ := c.Begin("k", "fp"); o != Miss {
		t.Fatalf("got %v, want Miss after Abort", o)
	}
}

func TestBegin_EntryExpiresAfterWindow(t *testing.T) {
	c, clk := newCache(10)
	c.Begin("k", "fp")
	c.Commit("k", "r")
	clk.t = clk.t.Add(10*time.Minute + time.Second)
	if o, _ := c.Begin("k", "fp"); o != Miss {
		t.Fatalf("got %v, want Miss after window", o)
	}
}

func TestCommit_BoundedEvictsOldest(t *testing.T) {
	c, clk := newCache(3)
	for _, k := range []string{"a", "b", "c", "d"} {
		c.Begin(k, "fp")
		c.Commit(k, k)
		clk.t = clk.t.Add(time.Second)
	}
	if c.Len() > 3 {
		t.Fatalf("len = %d, want <= 3", c.Len())
	}
	if o, _ := c.Begin("a", "fp"); o != Miss {
		t.Fatalf("oldest entry should have been evicted, got %v", o)
	}
	if o, _ := c.Begin("d", "fp"); o != Hit {
		t.Fatalf("newest entry should remain, got %v", o)
	}
}

func TestBegin_FullOfInFlightRefusesNewKeys(t *testing.T) {
	c, _ := newCache(2)
	c.Begin("a", "fp")
	c.Begin("b", "fp")
	if o, _ := c.Begin("c", "fp"); o != Full {
		t.Fatalf("got %v, want Full", o)
	}
}

func TestMarkUncertain_KeepsKeyHeldUntilWindowEnds(t *testing.T) {
	c, clk := newCache(10)
	c.Begin("k", "fp")
	c.MarkUncertain("k", "partial 1 of 2")
	if o, v := c.Begin("k", "fp"); o != Uncertain || v != "partial 1 of 2" {
		t.Fatalf("got %v %q", o, v)
	}
	if o, _ := c.Begin("k", "other"); o != Mismatch {
		t.Fatalf("different content = %v", o)
	}
	c.Abort("k") // Abort must not release a finished (uncertain) entry
	if o, _ := c.Begin("k", "fp"); o != Uncertain {
		t.Fatalf("after Abort = %v", o)
	}
	clk.t = clk.t.Add(11 * time.Minute)
	if o, _ := c.Begin("k", "fp"); o != Miss {
		t.Fatalf("after window = %v", o)
	}
}
