package httpx

import (
	"errors"
	"strings"
	"testing"
)

func TestReadBody_LimitIsInclusive(t *testing.T) {
	if b, err := ReadBody(strings.NewReader(strings.Repeat("a", MaxBody))); err != nil || len(b) != MaxBody {
		t.Fatalf("len=%d err=%v", len(b), err)
	}
	if _, err := ReadBody(strings.NewReader(strings.Repeat("a", MaxBody+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
}
