package bridge

import "testing"

func TestMemoryStore_GetSetDelete(t *testing.T) {
	s := NewMemoryStore()
	if got := s.Get(1); got != "" {
		t.Fatalf("empty store returned %q", got)
	}
	s.Set(1, "ctx-a")
	s.Set(2, "ctx-b")
	if s.Get(1) != "ctx-a" || s.Get(2) != "ctx-b" {
		t.Fatal("chats must have independent contexts")
	}
	s.Delete(1)
	if s.Get(1) != "" || s.Get(2) != "ctx-b" {
		t.Fatal("delete must only affect one chat")
	}
}
