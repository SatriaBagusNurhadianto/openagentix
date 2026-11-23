package ratelimit

import (
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func TestAllow_BurstUpToPerMinuteThenRefuses(t *testing.T) {
	c := newClock()
	l := New(3, 100, c.now)
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow(); !ok {
			t.Fatalf("call %d refused", i)
		}
	}
	ok, retry := l.Allow()
	if ok {
		t.Fatal("4th call must be refused")
	}
	if retry != 20*time.Second { // 3/min refills one token every 20s
		t.Fatalf("retry = %v, want 20s", retry)
	}
}

func TestAllow_RefillsOverTime(t *testing.T) {
	c := newClock()
	l := New(3, 100, c.now)
	for i := 0; i < 3; i++ {
		l.Allow()
	}
	c.advance(20 * time.Second)
	if ok, _ := l.Allow(); !ok {
		t.Fatal("one token should have refilled")
	}
	if ok, _ := l.Allow(); ok {
		t.Fatal("only one token should have refilled")
	}
}

func TestAllow_HourlyBudgetBindsEvenWhenMinuteRefills(t *testing.T) {
	c := newClock()
	l := New(10, 4, c.now)
	for i := 0; i < 4; i++ {
		if ok, _ := l.Allow(); !ok {
			t.Fatalf("call %d refused", i)
		}
	}
	c.advance(2 * time.Minute) // minute bucket full again, hour bucket not
	ok, retry := l.Allow()
	if ok {
		t.Fatal("hourly budget must refuse")
	}
	if retry != 15*time.Minute-2*time.Minute {
		t.Fatalf("retry = %v, want 13m (4/h refills one token per 15m)", retry)
	}
}

func TestAllow_RefusedCallConsumesNothing(t *testing.T) {
	c := newClock()
	l := New(1, 100, c.now)
	l.Allow()
	for i := 0; i < 5; i++ {
		l.Allow() // refused attempts must not push the retry time out
	}
	if _, retry := l.Allow(); retry != time.Minute {
		t.Fatalf("retry = %v, want 1m", retry)
	}
}

func TestAllow_ZeroMeansUnlimited(t *testing.T) {
	l := New(0, 0, newClock().now)
	for i := 0; i < 1000; i++ {
		if ok, _ := l.Allow(); !ok {
			t.Fatal("unlimited limiter refused")
		}
	}
}

func TestAllow_ConcurrentCallsNeverExceedBurst(t *testing.T) {
	l := New(10, 100, newClock().now)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Allow(); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 10 {
		t.Fatalf("allowed = %d, want 10", allowed)
	}
}

func TestAllowN_ConsumesNTokensAtomically(t *testing.T) {
	c := newClock()
	l := New(5, 100, c.now)
	if ok, _ := l.AllowN(3); !ok {
		t.Fatal("3 of 5 must pass")
	}
	ok, retry := l.AllowN(3) // only 2 left: refuse and consume nothing
	if ok || retry <= 0 {
		t.Fatalf("ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.AllowN(2); !ok {
		t.Fatal("the refused call must not have consumed anything")
	}
}

func TestAllowN_MoreThanCapacityIsClampedNotStuck(t *testing.T) {
	c := newClock()
	l := New(5, 100, c.now)
	if ok, _ := l.AllowN(50); !ok {
		t.Fatal("a full bucket must admit an oversized request as a whole bucket")
	}
	if ok, retry := l.AllowN(50); ok || retry > time.Minute {
		t.Fatalf("ok=%v retry=%v", ok, retry)
	}
}

func TestRefund_GivesTokensBackUpToCapacity(t *testing.T) {
	c := newClock()
	l := New(2, 100, c.now)
	l.Allow()
	l.Allow()
	l.Refund(1)
	if ok, _ := l.Allow(); !ok {
		t.Fatal("refunded token must be usable")
	}
	l.Refund(50)
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow(); !ok {
			t.Fatalf("call %d", i)
		}
	}
	if ok, _ := l.Allow(); ok {
		t.Fatal("refund must not exceed capacity")
	}
}
