package kb

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/kb/internal/store"
)

// chainOf builds a path through entities named by letters ("ABC" is A-B-C) from
// the given edge ids and documents. Entity ids are the letters' positions in the
// alphabet, so equal letters are equal entities.
func chainOf(coll string, seedScore float64, nodes string, edges []int64, docs []string) cand {
	p := store.Path{}
	for _, r := range nodes {
		p.Entities = append(p.Entities, store.Entity{ID: int64(r-'A') + 1, Name: string(r), Collection: coll})
	}
	for i, id := range edges {
		p.Steps = append(p.Steps, store.Step{Edge: store.Relation{ID: id}, Forward: true, Cite: store.Citation{DocumentID: docs[i]}})
	}
	return cand{Coll: coll, Path: p, SeedScore: seedScore}
}

func names(cs []cand) []string {
	var out []string
	for _, c := range cs {
		var n []string
		for _, e := range c.Path.Entities {
			n = append(n, e.Name)
		}
		out = append(out, strings.Join(n, ""))
	}
	return out
}

func TestRankDropsChainsThatAreInsideLongerOnes(t *testing.T) {
	long := chainOf("c", 1, "ABCD", []int64{1, 2, 3}, []string{"x", "y", "z"})
	prefix := chainOf("c", 1, "ABC", []int64{1, 2}, []string{"x", "y"})
	one := chainOf("c", 1, "AB", []int64{1}, []string{"x"})
	rev := chainOf("c", 1, "DCBA", []int64{3, 2, 1}, []string{"z", "y", "x"}) // the same walk from the other end
	inner := chainOf("c", 0.5, "BCD", []int64{2, 3}, []string{"y", "z"})      // from another seed, inside the long walk
	other := chainOf("c", 1, "AE", []int64{9}, []string{"x"})
	got := rankChains([]cand{prefix, one, rev, inner, other, long}, 10)
	if fmt.Sprint(names(got)) != "[ABCD AE]" {
		t.Fatalf("got %v", names(got))
	}
}

func TestRankPrefersChainsSpanningMoreDocuments(t *testing.T) {
	// two 3-edge chains from the same seed, one citing one document three times and one citing three documents
	flat := chainOf("c", 1, "ABCD", []int64{1, 2, 3}, []string{"x", "x", "x"})
	wide := chainOf("c", 1, "AEFG", []int64{7, 8, 9}, []string{"p", "q", "r"})
	short := chainOf("c", 1, "AH", []int64{20}, []string{"s"})
	got := rankChains([]cand{short, flat, wide}, 10)
	if fmt.Sprint(names(got)) != "[AEFG ABCD AH]" {
		t.Fatalf("got %v", names(got))
	}
	// documents beat length: 2 documents in 2 edges outrank 1 document in 3 edges
	two := chainOf("c", 1, "AIJ", []int64{30, 31}, []string{"p", "q"})
	got = rankChains([]cand{flat, two}, 10)
	if fmt.Sprint(names(got)) != "[AIJ ABCD]" {
		t.Fatalf("got %v", names(got))
	}
}

func TestRankOrderIsLongerThenSeedScoreThenStable(t *testing.T) {
	a := chainOf("c", 0.9, "AB", []int64{4}, []string{"x"})
	b := chainOf("c", 0.95, "BC", []int64{3}, []string{"y"})
	c := chainOf("c", 0.95, "CD", []int64{2}, []string{"z"})
	d := chainOf("c", 0.9, "DEF", []int64{5, 6}, []string{"x", "x"}) // 1 document, 2 edges
	got := rankChains([]cand{a, b, c, d}, 10)
	// one document each: the longer first, then the higher seed score, then the lower edge ids
	if fmt.Sprint(names(got)) != "[DEF CD BC AB]" {
		t.Fatalf("got %v", names(got))
	}
	again := rankChains([]cand{d, c, b, a}, 10)
	if !slices.Equal(names(got), names(again)) {
		t.Fatalf("order depends on the input order: %v vs %v", names(got), names(again))
	}
}

func TestRankCapsChainsPerSeedButFillsUnusedSlots(t *testing.T) {
	var cs []cand
	for i := int64(0); i < 6; i++ { // seed A has six chains
		cs = append(cs, chainOf("c", 1, "A"+string(rune('K'+i)), []int64{10 + i}, []string{fmt.Sprint("a", i)}))
	}
	cs = append(cs, chainOf("c", 0.5, "ZY", []int64{50}, []string{"z"}), chainOf("c", 0.5, "ZX", []int64{51}, []string{"z2"}))
	got := names(rankChains(cs, 4))
	// four slots, two seeds: two each, although seed A ranks better
	if !slices.Contains(got, "ZY") || !slices.Contains(got, "ZX") || len(got) != 4 {
		t.Fatalf("per-seed cap: %v", got)
	}
	// one seed with chains and the other with fewer than its share: the slots are not wasted
	got = names(rankChains(cs[:7], 4))
	if len(got) != 4 {
		t.Fatalf("unused slots must be filled: %v", got)
	}
}

func TestRankOfNothingIsNothing(t *testing.T) {
	if got := rankChains(nil, 10); len(got) != 0 {
		t.Fatal(got)
	}
}

func hit(id int64, name string, score float64) store.EntityHit {
	return store.EntityHit{Entity: store.Entity{ID: id, Name: name, Collection: "c"}, Score: score}
}

func seedNames(ss []seed) string {
	var n []string
	for _, s := range ss {
		n = append(n, s.Entity.Name)
	}
	return strings.Join(n, ",")
}

func TestSelectSeedsPrefersNamesInTheQueryOverEmbeddingMatches(t *testing.T) {
	lex := []store.EntityHit{hit(1, "Vane-3", 0.8)}
	emb := []store.EntityHit{hit(2, "network outage", 0.45), hit(3, "power outage on 3 March", 0.62), hit(4, "Teal", 0.9)}
	got := selectSeeds(lex, emb, 5)
	// Vane-3 is the only name in the query. Teal is a strong embedding match and joins; of the other two, the one above the
	// floor fills a seat below SeedLexicalTarget and the one under the floor never does.
	if seedNames(got) != "Vane-3,Teal,power outage on 3 March" {
		t.Fatalf("seeds = %s", seedNames(got))
	}
	if !got[0].Lexical || got[1].Lexical {
		t.Fatalf("lexical flags: %+v", got)
	}
}

func TestSelectSeedsAddsNoWeakEmbeddingSeedWhenEnoughNamesMatch(t *testing.T) {
	lex := []store.EntityHit{hit(1, "A", 1), hit(2, "B", 0.9), hit(3, "C", 0.8)}
	emb := []store.EntityHit{hit(9, "weak", 0.7), hit(8, "strong", SeedEmbeddingStrong)}
	got := selectSeeds(lex, emb, 5)
	if seedNames(got) != "A,B,C,strong" {
		t.Fatalf("seeds = %s", seedNames(got))
	}
}

func TestSelectSeedsIsBoundedDedupedAndIgnoresGenericContainmentMatches(t *testing.T) {
	lex := []store.EntityHit{hit(1, "database", 0.75), hit(2, "Orion", 0.75), hit(3, "database", 1)}
	emb := []store.EntityHit{hit(2, "Orion", 0.99), hit(5, "E", 0.99), hit(6, "F", 0.99)}
	got := selectSeeds(lex, emb, 3)
	// a generic entity named inside the query (0.75, not an exact match) is no seed; an exact one is
	if seedNames(got) != "database,Orion,E" || got[0].Entity.ID != 3 {
		t.Fatalf("seeds = %s", seedNames(got))
	}
	if selectSeeds(nil, nil, 5) != nil && len(selectSeeds(nil, nil, 5)) != 0 {
		t.Fatal("no hits, no seeds")
	}
}
