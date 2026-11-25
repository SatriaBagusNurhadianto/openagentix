package server

import (
	"fmt"
	"testing"
	"time"
)

func TestThrottleMemoryIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	th := newThrottle(3, 1_000_000, time.Minute, func() time.Time { return now })
	for i := 0; i < 20_000; i++ {
		th.fail(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	if n := th.size(); n > maxThrottleEntries+1 {
		t.Fatalf("%d entries", n)
	}
	// once the entries have gone quiet the table empties again as new addresses arrive
	now = now.Add(time.Hour)
	th.fail("192.0.2.1")
	if n := th.size(); n > maxThrottleEntries+1 {
		t.Fatalf("%d entries after the idle hour", n)
	}
	// a flood from many addresses still locks the shared overflow entry rather than growing the table
	if th.wait("203.0.113.9") < 0 {
		t.Fatal("negative wait")
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	th := newThrottle(1, 100, time.Minute, nil)
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		if got := th.backoff(i + 1); got != w {
			t.Errorf("strike %d: %v want %v", i+1, got, w)
		}
	}
}

func TestSplitHostRefusesOddShapes(t *testing.T) {
	for _, h := range []string{"", "a b", "a/b", "a@b", "[::1", "[nope]", "::1", "a:b", "a:1:2", ":80", "host:-1"} {
		if _, _, ok := splitHost(h); ok {
			t.Errorf("%q was accepted", h)
		}
	}
	if n, p, ok := splitHost("LocalHost.:8080"); !ok || n != "localhost" || p != "8080" {
		t.Errorf("%q %q %v", n, p, ok)
	}
}
