package history

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAppendBoundedByMaxTurns(t *testing.T) {
	s := NewStore(2, 10)
	for _, u := range []string{"a", "b", "c"} {
		s.Append("c1", Turn{User: u, Assistant: u + "!"})
	}
	got := s.Turns("c1")
	if len(got) != 2 || got[0].User != "b" || got[1].User != "c" {
		t.Fatalf("want last 2 turns [b c], got %+v", got)
	}
}

func TestTurnsReturnsCopy(t *testing.T) {
	s := NewStore(5, 10)
	s.Append("c1", Turn{User: "a", Assistant: "A"})
	s.Turns("c1")[0].User = "mutated"
	if s.Turns("c1")[0].User != "a" {
		t.Fatal("Turns must return a copy")
	}
}

func TestLockSerializesSameContext(t *testing.T) {
	s := NewStore(5, 10)
	unlock, err := s.Lock(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		u2, _ := s.Lock(context.Background(), "c1")
		close(acquired)
		u2()
	}()
	select {
	case <-acquired:
		t.Fatal("second Lock acquired while first held")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second Lock never acquired after unlock")
	}
}

func TestLockRespectsContext(t *testing.T) {
	s := NewStore(5, 10)
	unlock, _ := s.Lock(context.Background(), "c1")
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Lock(ctx, "c1"); err == nil {
		t.Fatal("want ctx error")
	}
}

func TestDifferentContextsDoNotBlock(t *testing.T) {
	s := NewStore(5, 10)
	u1, _ := s.Lock(context.Background(), "c1")
	defer u1()
	done := make(chan struct{})
	go func() { u, _ := s.Lock(context.Background(), "c2"); u(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("c2 blocked by c1")
	}
}

func TestEvictsLeastRecentlyUsedUnlocked(t *testing.T) {
	s := NewStore(5, 2)
	s.Append("a", Turn{User: "1"})
	s.Append("b", Turn{User: "2"})
	s.Turns("a") // touch a, so b is LRU
	s.Append("c", Turn{User: "3"})
	if len(s.Turns("b")) != 0 {
		t.Fatal("b should have been evicted")
	}
	if len(s.Turns("a")) != 1 || len(s.Turns("c")) != 1 {
		t.Fatal("a and c should remain")
	}
}

func TestConcurrentAppendRace(t *testing.T) {
	s := NewStore(5, 3)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Append("x", Turn{User: "u"})
			s.Turns("x")
		}()
	}
	wg.Wait()
}
