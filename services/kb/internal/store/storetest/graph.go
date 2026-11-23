package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/kb/internal/store"
)

// graphFixture seeds documents d1 and d2 in collection "g" and returns a helper
// that creates entities and relations.
type graphFixture struct {
	t   *testing.T
	ctx context.Context
	s   store.Store
}

func (g graphFixture) ent(coll, name, typ string) store.Entity {
	g.t.Helper()
	e, err := g.s.UpsertEntity(g.ctx, store.EntityInput{Collection: coll, Name: name, Type: typ})
	if err != nil {
		g.t.Fatalf("UpsertEntity(%s): %v", name, err)
	}
	return e
}

func (g graphFixture) rel(coll string, src, dst store.Entity, pred, doc string, chunk int) int64 {
	g.t.Helper()
	id, err := g.s.AddRelation(g.ctx, store.Relation{
		Collection: coll, SrcID: src.ID, DstID: dst.ID, Predicate: pred,
		Evidence: "evidence for " + pred, DocumentID: doc, ChunkIndex: chunk, PromptVersion: "v1", Model: "m",
	})
	if err != nil {
		g.t.Fatalf("AddRelation(%s): %v", pred, err)
	}
	return id
}

func (g graphFixture) doc(coll, id string, chunks ...string) {
	g.t.Helper()
	if err := g.s.UpsertDocument(g.ctx, doc(coll, id, id), ch(chunks...)); err != nil {
		g.t.Fatal(err)
	}
}

func names(p store.Path) string {
	n := make([]string, len(p.Entities))
	for i, e := range p.Entities {
		n[i] = e.Name
	}
	return strings.Join(n, ">")
}

func pathNames(ps []store.Path) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = names(p)
	}
	return out
}

func runGraph(t *testing.T, f Factory, run func(string, func(*testing.T, store.Store))) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	run("graph: upsert merges by normalised name within one collection and type", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		a, err := s.UpsertEntity(ctx, store.EntityInput{Collection: "g", Name: "Payments  Service", Type: "service", Aliases: []string{"Payments Svc"}})
		if err != nil {
			t.Fatal(err)
		}
		if a.ID == 0 || a.NormName != "payments service" || a.Origin != store.OriginExtracted || a.Collection != "g" {
			t.Fatalf("entity = %+v", a)
		}
		b, err := s.UpsertEntity(ctx, store.EntityInput{Collection: "g", Name: " payments service ", Type: "service", Description: "Handles payments", Aliases: []string{"pay-svc", "payments svc"}})
		if err != nil {
			t.Fatal(err)
		}
		if b.ID != a.ID || b.Name != "Payments  Service" && b.Name != "Payments Service" {
			t.Fatalf("not merged: %+v vs %+v", b, a)
		}
		if b.Description != "Handles payments" {
			t.Fatalf("empty description not filled: %+v", b)
		}
		if !slices.Equal(b.Aliases, []string{"pay svc", "payments svc"}) {
			t.Fatalf("aliases not united (match form, sorted, unique): %v", b.Aliases)
		}
		c, _ := s.UpsertEntity(ctx, store.EntityInput{Collection: "g", Name: "payments service", Type: "service", Description: "other"})
		if c.Description != "Handles payments" {
			t.Fatalf("existing description overwritten: %+v", c)
		}
		if d := g.ent("g", "payments service", "team"); d.ID == a.ID {
			t.Fatal("different type merged")
		}
		if d := g.ent("other", "payments service", "service"); d.ID == a.ID || d.Collection != "other" {
			t.Fatal("entities shared across collections")
		}
		if _, err := s.UpsertEntity(ctx, store.EntityInput{Collection: "g", Name: "  ", Type: "service"}); err == nil {
			t.Fatal("blank name accepted")
		}
	})

	run("graph: relation needs endpoints in its collection and an existing document", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "x")
		g.doc("o", "d1", "x")
		a, b := g.ent("g", "A", "service"), g.ent("g", "B", "service")
		foreign := g.ent("o", "F", "service")
		r := store.Relation{Collection: "g", SrcID: a.ID, DstID: foreign.ID, Predicate: "uses", Evidence: "e", DocumentID: "d1"}
		if _, err := s.AddRelation(ctx, r); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-collection relation: %v", err)
		}
		r = store.Relation{Collection: "g", SrcID: a.ID, DstID: b.ID, Predicate: "uses", Evidence: "e", DocumentID: "nope"}
		if _, err := s.AddRelation(ctx, r); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("relation of missing document: %v", err)
		}
		r.DocumentID = "d1"
		id1, err := s.AddRelation(ctx, r)
		if err != nil || id1 == 0 {
			t.Fatalf("id=%d err=%v", id1, err)
		}
		id2, err := s.AddRelation(ctx, r)
		if err != nil || id2 != id1 {
			t.Fatalf("repeat not idempotent: %d vs %d (%v)", id2, id1, err)
		}
		if st, _ := s.GraphStats(ctx, "g"); st.Relations != 1 {
			t.Fatalf("stats = %+v", st)
		}
	})

	run("graph: expand follows edges both ways with citations", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "c0", "c1")
		a, b, c := g.ent("g", "A", "service"), g.ent("g", "B", "service"), g.ent("g", "C", "team")
		g.rel("g", a, b, "calls", "d1", 0)
		g.rel("g", c, b, "owns", "d1", 1)
		ps, err := s.Expand(ctx, store.ExpandQuery{Collection: "g", SeedIDs: []int64{a.ID}, Hops: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(ps) != 1 || names(ps[0]) != "A>B" {
			t.Fatalf("1 hop = %v", pathNames(ps))
		}
		st := ps[0].Steps[0]
		if !st.Forward || st.Edge.Predicate != "calls" || st.Edge.SrcID != a.ID || st.Edge.DstID != b.ID {
			t.Fatalf("step = %+v", st)
		}
		if st.Cite != (store.Citation{DocumentID: "d1", ChunkIndex: 0, SourceURI: "https://example.test/d1"}) {
			t.Fatalf("citation = %+v", st.Cite)
		}
		if st.Edge.Evidence != "evidence for calls" || st.Edge.Model != "m" || st.Edge.PromptVersion != "v1" {
			t.Fatalf("edge = %+v", st.Edge)
		}
		ps, _ = s.Expand(ctx, store.ExpandQuery{Collection: "g", SeedIDs: []int64{a.ID}, Hops: 2})
		if got := pathNames(ps); !slices.Equal(got, []string{"A>B", "A>B>C"}) {
			t.Fatalf("2 hops = %v", got)
		}
		if ps[1].Steps[1].Forward || ps[1].Steps[1].Cite.ChunkIndex != 1 {
			t.Fatalf("against-direction step = %+v", ps[1].Steps[1])
		}
		if len(ps[1].Entities) != 3 || ps[1].Entities[2].Type != "team" {
			t.Fatalf("entities = %+v", ps[1].Entities)
		}
	})

	run("graph: expand is capped at 3 hops, has no cycles and honours the limit", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "c")
		var es []store.Entity
		for i := 0; i < 6; i++ {
			es = append(es, g.ent("g", fmt.Sprintf("N%d", i), "concept"))
		}
		for i := 0; i < 5; i++ {
			g.rel("g", es[i], es[i+1], "next", "d1", 0)
		}
		g.rel("g", es[2], es[0], "back", "d1", 0) // a cycle N0-N1-N2
		ps, err := s.Expand(ctx, store.ExpandQuery{Collection: "g", SeedIDs: []int64{es[0].ID}, Hops: 99})
		if err != nil {
			t.Fatal(err)
		}
		longest := 0
		for _, p := range ps {
			longest = max(longest, len(p.Steps))
			seen := map[int64]bool{}
			for _, e := range p.Entities {
				if seen[e.ID] {
					t.Fatalf("repeated entity in %s", names(p))
				}
				seen[e.ID] = true
			}
		}
		if longest != 3 {
			t.Fatalf("longest path has %d hops, want 3 (clamped): %v", longest, pathNames(ps))
		}
		for i := 1; i < len(ps); i++ {
			if len(ps[i].Steps) < len(ps[i-1].Steps) {
				t.Fatalf("not ordered by length: %v", pathNames(ps))
			}
		}
		again, _ := s.Expand(ctx, store.ExpandQuery{Collection: "g", SeedIDs: []int64{es[0].ID}, Hops: 99})
		if !slices.Equal(pathNames(again), pathNames(ps)) {
			t.Fatal("expand is not deterministic")
		}
		lim, _ := s.Expand(ctx, store.ExpandQuery{Collection: "g", SeedIDs: []int64{es[0].ID}, Hops: 3, Limit: 2})
		if len(lim) != 2 || names(lim[0]) != names(ps[0]) || names(lim[1]) != names(ps[1]) {
			t.Fatalf("limit: %v vs %v", pathNames(lim), pathNames(ps))
		}
		if none, err := s.Expand(ctx, store.ExpandQuery{Collection: "g", Hops: 2}); err != nil || len(none) != 0 {
			t.Fatalf("no seeds: %v %v", none, err)
		}
	})

	run("graph: expand follows at most FanOut edges per entity, lowest edge ids first", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "x")
		start, hub := g.ent("g", "Start", "service"), g.ent("g", "Hub", "service")
		g.rel("g", start, hub, "uses", "d1", 0)
		for i := 1; i <= 6; i++ {
			g.rel("g", hub, g.ent("g", fmt.Sprintf("N%d", i), "service"), "uses", "d1", 0)
		}
		// From Start, Hub is at depth 1 and its edges are the capped fan-out: the
		// edge back to Start counts, so FanOut 3 keeps Start, N1 and N2.
		ps, err := s.Expand(ctx, store.ExpandQuery{Collection: "g", SeedIDs: []int64{start.ID}, Hops: 2, FanOut: 3})
		if err != nil {
			t.Fatal(err)
		}
		if got := pathNames(ps); !slices.Equal(got, []string{"Start>Hub", "Start>Hub>N1", "Start>Hub>N2"}) {
			t.Fatalf("capped expand = %v", got)
		}
		// Seeded at the hub itself, the cap applies to the hub's own edges.
		ps, _ = s.Expand(ctx, store.ExpandQuery{Collection: "g", SeedIDs: []int64{hub.ID}, Hops: 1, FanOut: 2})
		if got := pathNames(ps); !slices.Equal(got, []string{"Hub>Start", "Hub>N1"}) {
			t.Fatalf("capped one-hop expand = %v", got)
		}
		// Without a value the default applies, which is larger than this graph.
		ps, _ = s.Expand(ctx, store.ExpandQuery{Collection: "g", SeedIDs: []int64{hub.ID}, Hops: 1})
		if len(ps) != 7 {
			t.Fatalf("default fan-out cut a small graph: %d paths", len(ps))
		}
		if store.ClampFanOut(0) != store.DefaultFanOut || store.ClampFanOut(1<<20) != store.MaxFanOut || store.ClampFanOut(5) != 5 {
			t.Fatal("ClampFanOut")
		}
	})

	run("graph: every graph query is scoped to its collection", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "c")
		g.doc("o", "d1", "c")
		a, b := g.ent("g", "Alpha", "service"), g.ent("g", "Beta", "service")
		oa, ob := g.ent("o", "Alpha", "service"), g.ent("o", "Beta", "service")
		g.rel("g", a, b, "calls", "d1", 0)
		g.rel("o", oa, ob, "calls", "d1", 0)
		ps, _ := s.Expand(ctx, store.ExpandQuery{Collection: "o", SeedIDs: []int64{a.ID}, Hops: 2})
		if len(ps) != 0 {
			t.Fatalf("seed of another collection expanded: %v", pathNames(ps))
		}
		ps, _ = s.Expand(ctx, store.ExpandQuery{Collection: "o", SeedIDs: []int64{a.ID, oa.ID}, Hops: 2})
		if len(ps) != 1 || ps[0].Entities[0].ID != oa.ID {
			t.Fatalf("mixed seeds: %v", pathNames(ps))
		}
		sp, _ := s.ShortestPaths(ctx, store.PathQuery{Collection: "o", FromID: a.ID, ToID: b.ID, MaxHops: 3})
		if len(sp) != 0 {
			t.Fatalf("shortest path across collections: %v", pathNames(sp))
		}
		hits, _ := s.FindEntitiesByName(ctx, "o", "alpha", 10)
		if len(hits) != 1 || hits[0].Entity.ID != oa.ID {
			t.Fatalf("find by name leaked: %+v", hits)
		}
		if st, _ := s.GraphStats(ctx, "o"); st.Entities != 2 || st.Relations != 1 {
			t.Fatalf("stats = %+v", st)
		}
	})

	run("graph: shortest paths returns all minimal paths only", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "c")
		a, b, c, d, e := g.ent("g", "A", "concept"), g.ent("g", "B", "concept"), g.ent("g", "C", "concept"), g.ent("g", "D", "concept"), g.ent("g", "E", "concept")
		g.rel("g", a, b, "x", "d1", 0)
		g.rel("g", b, d, "x", "d1", 0)
		g.rel("g", a, c, "x", "d1", 0)
		g.rel("g", c, d, "x", "d1", 0)
		g.rel("g", a, e, "x", "d1", 0) // A-E-B-D is longer
		g.rel("g", e, b, "x", "d1", 0)
		ps, err := s.ShortestPaths(ctx, store.PathQuery{Collection: "g", FromID: a.ID, ToID: d.ID, MaxHops: 3})
		if err != nil {
			t.Fatal(err)
		}
		if got := pathNames(ps); !slices.Equal(got, []string{"A>B>D", "A>C>D"}) {
			t.Fatalf("paths = %v", got)
		}
		if ps, _ = s.ShortestPaths(ctx, store.PathQuery{Collection: "g", FromID: a.ID, ToID: d.ID, MaxHops: 3, Limit: 1}); len(ps) != 1 {
			t.Fatalf("limit: %v", pathNames(ps))
		}
		if ps, _ = s.ShortestPaths(ctx, store.PathQuery{Collection: "g", FromID: a.ID, ToID: d.ID, MaxHops: 1}); len(ps) != 0 {
			t.Fatalf("beyond max hops: %v", pathNames(ps))
		}
		iso := g.ent("g", "Island", "concept")
		if ps, _ = s.ShortestPaths(ctx, store.PathQuery{Collection: "g", FromID: a.ID, ToID: iso.ID, MaxHops: 3}); len(ps) != 0 {
			t.Fatalf("unreachable: %v", pathNames(ps))
		}
		if ps, _ = s.ShortestPaths(ctx, store.PathQuery{Collection: "g", FromID: a.ID, ToID: a.ID, MaxHops: 3}); len(ps) != 0 {
			t.Fatalf("same entity: %v", pathNames(ps))
		}
	})

	run("graph: find entities by name or alias", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		pay, _ := s.UpsertEntity(ctx, store.EntityInput{Collection: "g", Name: "Payments Service", Type: "service", Aliases: []string{"Pay Svc"}})
		g.ent("g", "Payments", "concept")
		g.ent("g", "Billing Team", "team")
		g.ent("g", "x", "concept") // one character: never a containment match
		hits, err := s.FindEntitiesByName(ctx, "g", "Payments Service", 10)
		if err != nil || len(hits) == 0 || hits[0].Entity.ID != pay.ID {
			t.Fatalf("exact: %+v %v", hits, err)
		}
		hits, _ = s.FindEntitiesByName(ctx, "g", "who owns the payments service today?", 10)
		if len(hits) != 2 || hits[0].Entity.ID != pay.ID || hits[1].Entity.Name != "Payments" || hits[0].Score <= hits[1].Score {
			t.Fatalf("entity named in a question: %+v", hits)
		}
		hits, _ = s.FindEntitiesByName(ctx, "g", "pay svc", 10)
		if len(hits) != 1 || hits[0].Entity.ID != pay.ID {
			t.Fatalf("alias: %+v", hits)
		}
		hits, _ = s.FindEntitiesByName(ctx, "g", "billing", 10)
		if len(hits) != 1 || hits[0].Entity.Name != "Billing Team" {
			t.Fatalf("partial: %+v", hits)
		}
		if hits, _ = s.FindEntitiesByName(ctx, "g", "payments service", 1); len(hits) != 1 {
			t.Fatalf("limit: %+v", hits)
		}
		for _, q := range []string{"", "  ", "x"} {
			if hits, err = s.FindEntitiesByName(ctx, "g", q, 10); err != nil || len(hits) != 0 {
				t.Fatalf("q=%q: %+v %v", q, hits, err)
			}
		}
		if hits, err = s.FindEntitiesByName(ctx, "g", "'; DROP TABLE x; -- % _", 10); err != nil || len(hits) != 0 {
			t.Fatalf("hostile: %+v %v", hits, err)
		}
	})

	run("graph: replacing or deleting a document removes its graph rows", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "c")
		g.doc("g", "d2", "c")
		a, b, c := g.ent("g", "A", "concept"), g.ent("g", "B", "concept"), g.ent("g", "C", "concept")
		g.rel("g", a, b, "x", "d1", 0)
		g.rel("g", b, c, "y", "d2", 0)
		for _, e := range []store.Entity{a, b} {
			if err := s.LinkEntityChunk(ctx, "g", store.EntityChunk{EntityID: e.ID, DocumentID: "d1", ChunkIndex: 0}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.LinkEntityChunk(ctx, "g", store.EntityChunk{EntityID: c.ID, DocumentID: "d2", ChunkIndex: 0}); err != nil {
			t.Fatal(err)
		}
		if err := s.LinkEntityChunk(ctx, "g", store.EntityChunk{EntityID: c.ID, DocumentID: "d2", ChunkIndex: 0}); err != nil {
			t.Fatalf("link not idempotent: %v", err)
		}
		if st, _ := s.GraphStats(ctx, "g"); st.Entities != 3 || st.Relations != 2 || st.Mentions != 3 {
			t.Fatalf("before: %+v", st)
		}
		if err := s.DeleteDocumentGraph(ctx, "g", "d1"); err != nil {
			t.Fatal(err)
		}
		// A is only known from d1 and is collected; B is still the source of d2's relation.
		st, _ := s.GraphStats(ctx, "g")
		if st.Entities != 2 || st.Relations != 1 || st.Mentions != 1 {
			t.Fatalf("after DeleteDocumentGraph: %+v", st)
		}
		if ok, err := s.DeleteDocument(ctx, "g", "d2"); err != nil || !ok {
			t.Fatalf("delete: %v %v", ok, err)
		}
		if st, _ = s.GraphStats(ctx, "g"); st.Entities != 0 || st.Relations != 0 || st.Mentions != 0 {
			t.Fatalf("after DeleteDocument: %+v", st)
		}
	})

	run("graph: a manually created entity survives document deletion", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "c")
		keep, _ := s.UpsertEntity(ctx, store.EntityInput{Collection: "g", Name: "Keep", Type: "concept", Origin: "manual"})
		x := g.ent("g", "X", "concept")
		g.rel("g", keep, x, "x", "d1", 0)
		if _, err := s.DeleteDocument(ctx, "g", "d1"); err != nil {
			t.Fatal(err)
		}
		st, _ := s.GraphStats(ctx, "g")
		if st.Entities != 1 || st.Relations != 0 {
			t.Fatalf("stats = %+v", st)
		}
		hits, _ := s.FindEntitiesByName(ctx, "g", "keep", 5)
		if len(hits) != 1 || hits[0].Entity.Origin != "manual" {
			t.Fatalf("hits = %+v", hits)
		}
	})

	run("graph: document chunks", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "first", "second")
		got, err := s.DocumentChunks(ctx, "g", "d1")
		if err != nil || !slices.Equal(got, []string{"first", "second"}) {
			t.Fatalf("%v %v", got, err)
		}
		if _, err := s.DocumentChunks(ctx, "g", "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})

	specs := func(n int) []store.JobSpec {
		out := make([]store.JobSpec, n)
		for i := range out {
			out[i] = store.JobSpec{WindowIdx: i, WindowHash: fmt.Sprintf("h%d", i), FirstChunk: i, ChunkCount: 1}
		}
		return out
	}

	run("jobs: sync enqueues once and is a no-op for unchanged windows", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "a", "b")
		n, err := s.SyncExtractionJobs(ctx, "g", "d1", "v1", specs(2))
		if err != nil || n != 2 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		j, ok, err := s.ClaimJob(ctx, time.Minute)
		if err != nil || !ok || j.Collection != "g" || j.DocumentID != "d1" || j.WindowIdx != 0 || j.WindowHash != "h0" || j.ChunkCount != 1 || j.PromptVersion != "v1" || j.Attempts != 1 {
			t.Fatalf("claim: %+v ok=%v err=%v", j, ok, err)
		}
		if err := s.ApplyExtraction(ctx, store.ExtractionResult{Job: j.JobKey, Model: "m", PromptVersion: "v1"}); err != nil {
			t.Fatal(err)
		}
		n, err = s.SyncExtractionJobs(ctx, "g", "d1", "v1", specs(2))
		if err != nil || n != 0 {
			t.Fatalf("unchanged re-sync enqueued %d (%v)", n, err)
		}
		if st, _ := s.GraphStats(ctx, "g"); st.JobsDone != 1 || st.JobsPending != 1 {
			t.Fatalf("done job was reset: %+v", st)
		}
		// New prompt version or new window hashes: everything is redone.
		if n, _ = s.SyncExtractionJobs(ctx, "g", "d1", "v2", specs(2)); n != 2 {
			t.Fatalf("prompt version change enqueued %d", n)
		}
		ch := specs(2)
		ch[1].WindowHash = "changed"
		if n, _ = s.SyncExtractionJobs(ctx, "g", "d1", "v2", ch); n != 2 {
			t.Fatalf("content change enqueued %d", n)
		}
		if st, _ := s.GraphStats(ctx, "g"); st.JobsDone != 0 || st.JobsPending != 2 {
			t.Fatalf("stats = %+v", st)
		}
		if _, err := s.SyncExtractionJobs(ctx, "g", "missing", "v1", specs(1)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("job for a missing document: %v", err)
		}
		if n, err = s.SyncExtractionJobs(ctx, "g", "d1", "v2", nil); err != nil || n != 0 {
			t.Fatalf("empty specs: %d %v", n, err)
		}
		if _, ok, _ := s.ClaimJob(ctx, time.Minute); ok {
			t.Fatal("jobs left after syncing an empty window list")
		}
	})

	run("jobs: sync replaces the old graph rows of a changed document", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "a")
		g.doc("g", "d2", "a")
		a, b, c := g.ent("g", "A", "concept"), g.ent("g", "B", "concept"), g.ent("g", "C", "concept")
		g.rel("g", a, b, "x", "d1", 0)
		g.rel("g", b, c, "y", "d2", 0)
		if _, err := s.SyncExtractionJobs(ctx, "g", "d1", "v1", specs(1)); err != nil {
			t.Fatal(err)
		}
		st, _ := s.GraphStats(ctx, "g")
		if st.Relations != 1 {
			t.Fatalf("d1's relation not removed: %+v", st)
		}
		if st.Entities != 2 {
			t.Fatalf("orphan entity A not collected, or B/C lost: %+v", st)
		}
	})

	run("jobs: claim leases, reclaims after expiry, retries and fails", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "a")
		if _, err := s.SyncExtractionJobs(ctx, "g", "d1", "v1", specs(1)); err != nil {
			t.Fatal(err)
		}
		j, ok, _ := s.ClaimJob(ctx, 150*time.Millisecond)
		if !ok || j.Attempts != 1 {
			t.Fatalf("claim: %+v %v", j, ok)
		}
		if _, ok, _ := s.ClaimJob(ctx, time.Minute); ok {
			t.Fatal("a leased job was claimed twice")
		}
		time.Sleep(200 * time.Millisecond) // the worker "died": the lease expires
		j, ok, _ = s.ClaimJob(ctx, time.Minute)
		if !ok || j.Attempts != 2 {
			t.Fatalf("reclaim: %+v %v", j, ok)
		}
		if err := s.FailJob(ctx, j.JobKey, "http_429", 150*time.Millisecond, false); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.ClaimJob(ctx, time.Minute); ok {
			t.Fatal("job claimed before its retry time")
		}
		time.Sleep(200 * time.Millisecond)
		j, ok, _ = s.ClaimJob(ctx, time.Minute)
		if !ok || j.Attempts != 3 || j.LastErrorCode != "http_429" {
			t.Fatalf("retry claim: %+v %v", j, ok)
		}
		if err := s.FailJob(ctx, j.JobKey, "malformed_output", 0, true); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.ClaimJob(ctx, time.Minute); ok {
			t.Fatal("failed job claimed")
		}
		if st, _ := s.GraphStats(ctx, "g"); st.JobsFailed != 1 || st.JobsPending != 0 || st.LastErrorCode != "malformed_output" {
			t.Fatalf("stats = %+v", st)
		}
		if st, _ := s.GraphStats(ctx, "elsewhere"); st.LastErrorCode != "" {
			t.Fatalf("error code leaked across collections: %+v", st)
		}
		if err := s.ApplyExtraction(ctx, store.ExtractionResult{Job: j.JobKey}); !errors.Is(err, store.ErrJobGone) {
			t.Fatalf("apply to a failed job: %v", err)
		}
	})

	run("jobs: apply stores the result atomically and only once", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "c0", "c1")
		if _, err := s.SyncExtractionJobs(ctx, "g", "d1", "v1", []store.JobSpec{{WindowIdx: 0, WindowHash: "h", FirstChunk: 0, ChunkCount: 2}}); err != nil {
			t.Fatal(err)
		}
		j, _, _ := s.ClaimJob(ctx, time.Minute)
		res := store.ExtractionResult{
			Job: j.JobKey, Model: "gpt-x", PromptVersion: "v1",
			Entities: []store.ExtractedEntity{
				{Name: "Checkout", Type: "service", Description: "d", Chunks: []int{0, 1}},
				{Name: "Payments", Type: "service", Chunks: []int{1}},
			},
			Relations: []store.ExtractedRelation{{Src: 0, Dst: 1, Predicate: "calls", Evidence: "Checkout calls Payments", ChunkIndex: 1}},
		}
		if err := s.ApplyExtraction(ctx, res); err != nil {
			t.Fatal(err)
		}
		if st, _ := s.GraphStats(ctx, "g"); st.Entities != 2 || st.Relations != 1 || st.Mentions != 3 || st.JobsDone != 1 {
			t.Fatalf("stats = %+v", st)
		}
		if err := s.ApplyExtraction(ctx, res); !errors.Is(err, store.ErrJobGone) {
			t.Fatalf("second apply: %v", err)
		}
		if st, _ := s.GraphStats(ctx, "g"); st.Entities != 2 || st.Relations != 1 || st.Mentions != 3 {
			t.Fatalf("second apply changed rows: %+v", st)
		}
		hits, _ := s.FindEntitiesByName(ctx, "g", "checkout", 5)
		ps, _ := s.Expand(ctx, store.ExpandQuery{Collection: "g", SeedIDs: []int64{hits[0].Entity.ID}, Hops: 1})
		if len(ps) != 1 || ps[0].Steps[0].Edge.Model != "gpt-x" || ps[0].Steps[0].Edge.PromptVersion != "v1" || ps[0].Steps[0].Cite.ChunkIndex != 1 {
			t.Fatalf("paths = %+v", ps)
		}
		// A bad reference rolls everything back.
		g.doc("g", "d2", "c")
		_, _ = s.SyncExtractionJobs(ctx, "g", "d2", "v1", specs(1))
		j2, _, _ := s.ClaimJob(ctx, time.Minute)
		bad := store.ExtractionResult{Job: j2.JobKey, Entities: []store.ExtractedEntity{{Name: "Z", Type: "concept"}},
			Relations: []store.ExtractedRelation{{Src: 0, Dst: 5, Predicate: "x", Evidence: "e"}}}
		if err := s.ApplyExtraction(ctx, bad); err == nil {
			t.Fatal("dangling relation index accepted")
		}
		if st, _ := s.GraphStats(ctx, "g"); st.Entities != 2 || st.JobsDone != 1 {
			t.Fatalf("failed apply left rows behind: %+v", st)
		}
	})

	run("jobs: deleting a document drops its jobs", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("g", "d1", "a")
		_, _ = s.SyncExtractionJobs(ctx, "g", "d1", "v1", specs(1))
		_, _ = s.DeleteDocument(ctx, "g", "d1")
		if _, ok, _ := s.ClaimJob(ctx, time.Minute); ok {
			t.Fatal("job of a deleted document claimed")
		}
		if st, _ := s.GraphStats(ctx, "g"); st.JobsPending != 0 {
			t.Fatalf("stats = %+v", st)
		}
	})

	if f.Vectors {
		run("graph: find entities by name embedding, scoped to the collection", func(t *testing.T, s store.Store) {
			mk := func(coll, name string, v []float32) store.Entity {
				e, err := s.UpsertEntity(ctx, store.EntityInput{Collection: coll, Name: name, Type: "concept", NameEmbedding: v})
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			x := mk("g", "X", []float32{1, 0, 0})
			mk("g", "Y", []float32{0, 1, 0})
			mk("g", "NoVec", nil)
			mk("o", "Other", []float32{1, 0, 0})
			hits, err := s.FindEntitiesByEmbedding(ctx, "g", []float32{0.9, 0.1, 0}, 5)
			if err != nil || len(hits) == 0 || hits[0].Entity.ID != x.ID || hits[0].Score <= 0 {
				t.Fatalf("hits = %+v err=%v", hits, err)
			}
			for _, h := range hits {
				if h.Entity.Collection != "g" || h.Entity.Name == "NoVec" {
					t.Fatalf("unexpected hit %+v", h)
				}
			}
			// A later merge fills a missing embedding.
			if _, err := s.UpsertEntity(ctx, store.EntityInput{Collection: "g", Name: "NoVec", Type: "concept", NameEmbedding: []float32{0, 0, 1}}); err != nil {
				t.Fatal(err)
			}
			hits, _ = s.FindEntitiesByEmbedding(ctx, "g", []float32{0, 0, 1}, 1)
			if len(hits) != 1 || hits[0].Entity.Name != "NoVec" {
				t.Fatalf("embedding not filled on merge: %+v", hits)
			}
			if _, err := s.UpsertEntity(ctx, store.EntityInput{Collection: "g", Name: "Bad", Type: "concept", NameEmbedding: []float32{1}}); err == nil {
				t.Fatal("wrong embedding dimension accepted")
			}
		})
	}
}
