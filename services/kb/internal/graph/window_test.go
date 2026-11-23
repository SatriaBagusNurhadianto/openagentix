package graph

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlanPacksWholeChunksUpToTheLimit(t *testing.T) {
	chunks := []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 100)}
	ws := Plan(chunks, 100)
	if len(ws) != 3 {
		t.Fatalf("windows = %d, want 3: %+v", len(ws), ws)
	}
	// 40 + "\n\n" + 40 = 82 <= 100; adding the third would be 124.
	if ws[0].FirstChunk != 0 || ws[0].ChunkCount != 2 || ws[1].FirstChunk != 2 || ws[1].ChunkCount != 1 || ws[2].FirstChunk != 3 || ws[2].ChunkCount != 1 {
		t.Fatalf("windows = %+v", ws)
	}
	for i, w := range ws {
		if w.Idx != i || w.Hash == "" || w.Text == "" {
			t.Fatalf("window %d = %+v", i, w)
		}
	}
	if ws[0].Text != chunks[0]+"\n\n"+chunks[1] {
		t.Fatalf("text = %q", ws[0].Text)
	}
}

func TestPlanOversizedChunkGetsItsOwnWindow(t *testing.T) {
	ws := Plan([]string{"short", strings.Repeat("x", 500), "tail"}, 100)
	if len(ws) != 3 || ws[1].ChunkCount != 1 || utf8.RuneCountInString(ws[1].Text) != 500 {
		t.Fatalf("%+v", ws)
	}
}

func TestPlanCountsRunesNotBytes(t *testing.T) {
	ws := Plan([]string{strings.Repeat("é", 60), strings.Repeat("é", 30)}, 100)
	if len(ws) != 1 {
		t.Fatalf("120 bytes but 92 runes must share a window: %d windows", len(ws))
	}
}

func TestPlanIsDeterministicAndHashFollowsTheText(t *testing.T) {
	a := Plan([]string{"one", "two", "three"}, 1000)
	b := Plan([]string{"one", "two", "three"}, 1000)
	c := Plan([]string{"one", "two", "THREE"}, 1000)
	if a[0].Hash != b[0].Hash || a[0].Hash == c[0].Hash {
		t.Fatalf("hashes: %s %s %s", a[0].Hash, b[0].Hash, c[0].Hash)
	}
	if Plan(nil, 100) != nil || len(Plan([]string{}, 100)) != 0 {
		t.Fatal("no chunks must give no windows")
	}
}

func TestChunkAtMapsOffsetsToChunks(t *testing.T) {
	w := Plan([]string{"alpha", "bravo", "charlie"}, 1000)[0]
	for off, want := range map[int]int{0: 0, 4: 0, 5: 0, 7: 1, 11: 1, 14: 2, len(w.Text) - 1: 2} {
		if got := w.ChunkAt(off); got != want {
			t.Errorf("ChunkAt(%d) = %d, want %d", off, got, want)
		}
	}
	w2 := Plan([]string{"x", "alpha", "bravo"}, 1)[1]
	if w2.FirstChunk != 1 || w2.ChunkAt(0) != 1 {
		t.Fatalf("%+v %d", w2, w2.ChunkAt(0))
	}
}

func TestSpecsMirrorWindows(t *testing.T) {
	ws := Plan([]string{"a", "b"}, 1)
	sp := Specs(ws)
	if len(sp) != 2 || sp[1].WindowIdx != 1 || sp[1].FirstChunk != 1 || sp[1].ChunkCount != 1 || sp[1].WindowHash != ws[1].Hash {
		t.Fatalf("%+v", sp)
	}
}
