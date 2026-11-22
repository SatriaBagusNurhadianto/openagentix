package chunk

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmptyAndWhitespaceYieldNoChunks(t *testing.T) {
	for _, in := range []string{"", "   \n\n\t "} {
		if got := Split(in, Options{MaxChars: 100}); len(got) != 0 {
			t.Fatalf("Split(%q) = %v, want none", in, got)
		}
	}
}

func TestShortTextIsOneChunk(t *testing.T) {
	got := Split("Hello world.", Options{MaxChars: 100, Overlap: 10})
	if len(got) != 1 || got[0] != "Hello world." {
		t.Fatalf("got %q", got)
	}
}

func TestSplitsOnParagraphBoundaries(t *testing.T) {
	p1 := strings.Repeat("a", 40)
	p2 := strings.Repeat("b", 40)
	p3 := strings.Repeat("c", 40)
	got := Split(p1+"\n\n"+p2+"\n\n"+p3, Options{MaxChars: 90})
	if len(got) != 2 || got[0] != p1+"\n\n"+p2 || got[1] != p3 {
		t.Fatalf("got %q", got)
	}
}

func TestSplitsLongParagraphOnSentences(t *testing.T) {
	text := "First sentence is here. Second sentence follows it. Third one ends the paragraph."
	got := Split(text, Options{MaxChars: 40})
	for _, c := range got {
		if utf8.RuneCountInString(c) > 40 {
			t.Fatalf("chunk too long: %q", c)
		}
		if !strings.HasSuffix(c, ".") {
			t.Fatalf("chunk does not end on a sentence boundary: %q", c)
		}
	}
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %q", got)
	}
}

func TestHardSplitsOversizedWord(t *testing.T) {
	got := Split(strings.Repeat("x", 250), Options{MaxChars: 100})
	if len(got) != 3 {
		t.Fatalf("got %d chunks: %q", len(got), got)
	}
	if strings.Join(got, "") != strings.Repeat("x", 250) {
		t.Fatal("content lost")
	}
}

func TestOverlapCarriesTail(t *testing.T) {
	text := "alpha beta gamma delta.\n\nepsilon zeta eta theta.\n\niota kappa lambda mu."
	got := Split(text, Options{MaxChars: 30, Overlap: 12})
	if len(got) < 3 {
		t.Fatalf("got %q", got)
	}
	for i := 1; i < len(got); i++ {
		prevTail := got[i-1][len(got[i-1])-6:]
		if !strings.Contains(got[i], strings.Fields(prevTail)[len(strings.Fields(prevTail))-1]) {
			t.Fatalf("chunk %d %q lacks overlap from %q", i, got[i], got[i-1])
		}
		if utf8.RuneCountInString(got[i]) > 30 {
			t.Fatalf("chunk too long: %q", got[i])
		}
	}
}

func TestUTF8Safe(t *testing.T) {
	text := strings.Repeat("héllo wörld 日本語テキスト ", 60)
	for _, c := range Split(text, Options{MaxChars: 50, Overlap: 10}) {
		if !utf8.ValidString(c) {
			t.Fatalf("invalid UTF-8 chunk: %q", c)
		}
		if utf8.RuneCountInString(c) > 50 {
			t.Fatalf("chunk over limit: %d", utf8.RuneCountInString(c))
		}
	}
	// no spaces at all: hard split must not cut runes
	for _, c := range Split(strings.Repeat("日", 205), Options{MaxChars: 100}) {
		if !utf8.ValidString(c) {
			t.Fatal("invalid UTF-8 on hard split")
		}
	}
}

func TestDeterministic(t *testing.T) {
	text := strings.Repeat("One sentence. Another sentence here.\n\nNew para. ", 50)
	o := Options{MaxChars: 80, Overlap: 20}
	a, b := Split(text, o), Split(text, o)
	if strings.Join(a, "|") != strings.Join(b, "|") {
		t.Fatal("non-deterministic")
	}
}

func TestDefaultsApplied(t *testing.T) {
	got := Split(strings.Repeat("word ", 1000), Options{})
	for _, c := range got {
		if utf8.RuneCountInString(c) > DefaultMaxChars {
			t.Fatalf("exceeds default: %d", utf8.RuneCountInString(c))
		}
	}
	if len(got) < 3 {
		t.Fatalf("got %d chunks", len(got))
	}
}

func TestCRLFNormalised(t *testing.T) {
	got := Split("a\r\n\r\nb", Options{MaxChars: 100})
	if len(got) != 1 || got[0] != "a\n\nb" {
		t.Fatalf("got %q", got)
	}
}

func TestOverlapClampedBelowMax(t *testing.T) {
	got := Split(strings.Repeat("word ", 100), Options{MaxChars: 20, Overlap: 50})
	for _, c := range got {
		if utf8.RuneCountInString(c) > 20 {
			t.Fatalf("too long: %q", c)
		}
	}
}
