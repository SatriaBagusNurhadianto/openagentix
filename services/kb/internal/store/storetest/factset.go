package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/openagentix/openagentix/services/kb/internal/store"
)

func catalogue(coll, id string) store.FactSet {
	return store.FactSet{
		Collection: coll, ID: id, SourceURI: "https://example.test/catalogue",
		Entities: []store.FactEntity{
			{Name: "Basalt", Type: "team", Description: "Platform team", Aliases: []string{"basalt squad"}},
			{Name: "Orion", Type: "service"},
			{Name: "Nadia Rao", Type: "person"},
		},
		Relations: []store.FactRelation{
			{Source: "Basalt", Predicate: "owns", Target: "Orion", Note: "Basalt owns Orion"},
			{Source: "Nadia Rao", Predicate: "manages", Target: "Basalt"},
		},
	}
}

func findOne(t *testing.T, ctx context.Context, s store.Store, coll, name string) store.Entity {
	t.Helper()
	hits, err := s.FindEntitiesByName(ctx, coll, name, 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("entity %q not found (%v)", name, err)
	}
	return hits[0].Entity
}

func hasEntity(ctx context.Context, s store.Store, coll, name string) bool {
	hits, _ := s.FindEntitiesByName(ctx, coll, name, 5)
	for _, h := range hits {
		if store.CanonicalForm(h.Entity.Name) == store.CanonicalForm(name) {
			return true
		}
	}
	return false
}

func runFactSets(t *testing.T, f Factory, run func(string, func(*testing.T, store.Store))) {
	ctx := context.Background()

	run("factset: replace stores curated facts that the graph queries return with a fact set citation", func(t *testing.T, s store.Store) {
		changed, err := s.ReplaceFactSet(ctx, catalogue("c", "org"))
		if err != nil || !changed {
			t.Fatalf("replace: changed=%v err=%v", changed, err)
		}
		got, err := s.GetFactSet(ctx, "c", "org")
		if err != nil {
			t.Fatal(err)
		}
		want := catalogue("c", "org")
		if got.SourceURI != want.SourceURI || !slices.Equal(got.Entities[0].Aliases, want.Entities[0].Aliases) || len(got.Entities) != 3 || len(got.Relations) != 2 || got.Relations[0].Note != "Basalt owns Orion" {
			t.Fatalf("GetFactSet = %+v", got)
		}
		infos, err := s.ListFactSets(ctx, "c")
		if err != nil || len(infos) != 1 || infos[0].ID != "org" || infos[0].Entities != 3 || infos[0].Relations != 2 || infos[0].SourceURI != want.SourceURI || infos[0].UpdatedAt.IsZero() {
			t.Fatalf("ListFactSets = %+v %v", infos, err)
		}
		if other, _ := s.ListFactSets(ctx, "elsewhere"); len(other) != 0 {
			t.Fatalf("fact sets leak across collections: %+v", other)
		}
		b := findOne(t, ctx, s, "c", "basalt squad")
		if b.Origin != store.OriginManual || b.Name != "Basalt" {
			t.Fatalf("entity = %+v", b)
		}
		ps, err := s.Expand(ctx, store.ExpandQuery{Collection: "c", SeedIDs: []int64{b.ID}, Hops: 1})
		if err != nil || len(ps) != 2 {
			t.Fatalf("expand: %v %v", ps, err)
		}
		for _, p := range ps {
			st := p.Steps[0]
			if st.Edge.Origin != store.OriginManual || st.Edge.FactsetID != "org" || st.Edge.DocumentID != "" ||
				st.Cite.FactsetID != "org" || st.Cite.DocumentID != "" || st.Cite.SourceURI != "https://example.test/catalogue" {
				t.Fatalf("step = %+v", st)
			}
			if st.Edge.Predicate == "owns" && st.Edge.Evidence != "Basalt owns Orion" {
				t.Fatalf("note not the evidence: %+v", st.Edge)
			}
		}
		st, err := s.GraphStats(ctx, "c")
		if err != nil || st.FactSets != 1 || st.ManualEntities != 3 || st.ManualRelations != 2 || st.Entities != 3 || st.Relations != 2 {
			t.Fatalf("stats = %+v %v", st, err)
		}
		if _, err := s.GetFactSet(ctx, "c", "nope"); !errors.Is(err, store.ErrFactSetNotFound) {
			t.Fatalf("missing: %v", err)
		}
		if _, err := s.GetFactSet(ctx, "other", "org"); !errors.Is(err, store.ErrFactSetNotFound) {
			t.Fatalf("other collection: %v", err)
		}
	})

	run("factset: replacing with the same content changes nothing", func(t *testing.T, s store.Store) {
		if _, err := s.ReplaceFactSet(ctx, catalogue("c", "org")); err != nil {
			t.Fatal(err)
		}
		b := findOne(t, ctx, s, "c", "Basalt")
		before, _ := s.Expand(ctx, store.ExpandQuery{Collection: "c", SeedIDs: []int64{b.ID}, Hops: 1})
		changed, err := s.ReplaceFactSet(ctx, catalogue("c", "org"))
		if err != nil || changed {
			t.Fatalf("second replace: changed=%v err=%v", changed, err)
		}
		after, _ := s.Expand(ctx, store.ExpandQuery{Collection: "c", SeedIDs: []int64{b.ID}, Hops: 1})
		if len(before) != len(after) || before[0].Steps[0].Edge.ID != after[0].Steps[0].Edge.ID {
			t.Fatal("an identical replace rewrote the relations")
		}
		changedSrc := catalogue("c", "org")
		changedSrc.SourceURI = "https://example.test/other"
		if changed, err := s.ReplaceFactSet(ctx, changedSrc); err != nil || !changed {
			t.Fatalf("a changed source uri is a change: %v %v", changed, err)
		}
	})

	run("factset: a changed body removes the facts that disappeared and renames what changed", func(t *testing.T, s store.Store) {
		if _, err := s.ReplaceFactSet(ctx, catalogue("c", "org")); err != nil {
			t.Fatal(err)
		}
		v2 := store.FactSet{Collection: "c", ID: "org", Entities: []store.FactEntity{
			{Name: "Team Basalt", Type: "team"}, {Name: "Orion", Type: "service"},
		}, Relations: []store.FactRelation{{Source: "Team Basalt", Predicate: "runs", Target: "Orion"}}}
		if changed, err := s.ReplaceFactSet(ctx, v2); err != nil || !changed {
			t.Fatalf("v2: %v %v", changed, err)
		}
		if hasEntity(ctx, s, "c", "Nadia Rao") {
			t.Fatal("an entity that left the fact set is still there")
		}
		b := findOne(t, ctx, s, "c", "Basalt")
		if b.Name != "Team Basalt" || b.Origin != store.OriginManual {
			t.Fatalf("the curated name does not follow the fact set: %+v", b)
		}
		ps, _ := s.Expand(ctx, store.ExpandQuery{Collection: "c", SeedIDs: []int64{b.ID}, Hops: 1})
		if len(ps) != 1 || ps[0].Steps[0].Edge.Predicate != "runs" {
			t.Fatalf("relations after v2: %v", pathNames(ps))
		}
		st, _ := s.GraphStats(ctx, "c")
		if st.Entities != 2 || st.Relations != 1 || st.ManualEntities != 2 || st.ManualRelations != 1 {
			t.Fatalf("stats = %+v", st)
		}
		got, _ := s.GetFactSet(ctx, "c", "org")
		if len(got.Entities) != 2 || got.Relations[0].Predicate != "runs" {
			t.Fatalf("stored body = %+v", got)
		}
	})

	run("factset: curated entities merge with extracted ones and win for name and type", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("c", "d1", "Basalt owns the Orion service")
		x := g.ent("c", "the Basalt group", "service") // the model's name and type
		o := g.ent("c", "Orion", "service")
		g.rel("c", x, o, "owns", "d1", 0)
		if _, err := s.ReplaceFactSet(ctx, catalogue("c", "org")); err != nil {
			t.Fatal(err)
		}
		b := findOne(t, ctx, s, "c", "Basalt")
		if b.ID != x.ID {
			t.Fatalf("not merged: %+v vs %+v", b, x)
		}
		if b.Name != "Basalt" || b.Type != "team" || b.Origin != store.OriginManual || !slices.Contains(b.OtherTypes, "service") {
			t.Fatalf("curated name, type and origin must win: %+v", b)
		}
		if b.Description != "Platform team" {
			t.Fatalf("description = %q", b.Description)
		}
		ps, _ := s.Expand(ctx, store.ExpandQuery{Collection: "c", SeedIDs: []int64{b.ID}, Hops: 1})
		docs, sets := 0, 0
		for _, p := range ps {
			if p.Steps[0].Cite.FactsetID != "" {
				sets++
			}
			if p.Steps[0].Cite.DocumentID == "d1" {
				docs++
			}
		}
		if docs != 1 || sets != 2 {
			t.Fatalf("extracted and curated facts must mix: %d document, %d curated", docs, sets)
		}
		// a mention extracted later ("Team Basalt" is more specific, but curated names do not change)
		if again := g.ent("c", "Team Basalt", "organization"); again.ID != b.ID {
			t.Fatalf("not merged: %+v", again)
		}
		if e := findOne(t, ctx, s, "c", "Basalt"); e.Name != "Basalt" || e.Type != "team" || e.Origin != store.OriginManual {
			t.Fatalf("extraction overrode curation: %+v", e)
		}
	})

	run("factset: deleting or re-ingesting documents never removes curated facts", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("c", "d1", "Basalt owns Orion and Quartz")
		if _, err := s.ReplaceFactSet(ctx, catalogue("c", "org")); err != nil {
			t.Fatal(err)
		}
		b, o := findOne(t, ctx, s, "c", "Basalt"), findOne(t, ctx, s, "c", "Orion")
		q := g.ent("c", "Quartz", "service")
		g.rel("c", b, q, "owns", "d1", 0)
		if err := s.LinkEntityChunk(ctx, "c", store.EntityChunk{EntityID: o.ID, DocumentID: "d1", ChunkIndex: 0}); err != nil {
			t.Fatal(err)
		}
		check := func(when string) {
			t.Helper()
			st, _ := s.GraphStats(ctx, "c")
			if st.ManualEntities != 3 || st.ManualRelations != 2 {
				t.Fatalf("%s: stats = %+v", when, st)
			}
			if !hasEntity(ctx, s, "c", "Nadia Rao") || findOne(t, ctx, s, "c", "Orion").Origin != store.OriginManual {
				t.Fatalf("%s: a curated entity is gone or un-marked", when)
			}
		}
		// re-ingest with different content: the document's graph is replaced
		if _, err := s.SyncExtractionJobs(ctx, "c", "d1", "v1", []store.JobSpec{{WindowIdx: 0, WindowHash: "h1", FirstChunk: 0, ChunkCount: 1}}); err != nil {
			t.Fatal(err)
		}
		check("after re-ingest")
		if hasEntity(ctx, s, "c", "Quartz") {
			t.Fatal("the extracted entity should have been collected")
		}
		if err := s.DeleteDocumentGraph(ctx, "c", "d1"); err != nil {
			t.Fatal(err)
		}
		check("after DeleteDocumentGraph")
		if ok, err := s.DeleteDocument(ctx, "c", "d1"); err != nil || !ok {
			t.Fatalf("delete document: %v %v", ok, err)
		}
		check("after DeleteDocument")
	})

	run("factset: deleting a fact set un-marks entities that extracted facts still use and removes the others", func(t *testing.T, s store.Store) {
		g := graphFixture{t, ctx, s}
		g.doc("c", "d1", "Basalt owns Orion")
		x := g.ent("c", "Basalt", "team")
		o := g.ent("c", "Orion", "service")
		g.rel("c", x, o, "owns", "d1", 0)
		if _, err := s.ReplaceFactSet(ctx, catalogue("c", "org")); err != nil {
			t.Fatal(err)
		}
		ok, err := s.DeleteFactSet(ctx, "c", "org")
		if err != nil || !ok {
			t.Fatalf("delete: %v %v", ok, err)
		}
		if ok, _ := s.DeleteFactSet(ctx, "c", "org"); ok {
			t.Fatal("second delete reports it existed")
		}
		if hasEntity(ctx, s, "c", "Nadia Rao") {
			t.Fatal("a curated-only entity survived the deletion of its fact set")
		}
		b := findOne(t, ctx, s, "c", "Basalt")
		if b.ID != x.ID || b.Origin != store.OriginExtracted {
			t.Fatalf("an entity extracted facts use must stay, un-marked: %+v", b)
		}
		ps, _ := s.Expand(ctx, store.ExpandQuery{Collection: "c", SeedIDs: []int64{b.ID}, Hops: 1})
		if len(ps) != 1 || ps[0].Steps[0].Cite.DocumentID != "d1" {
			t.Fatalf("only the document fact should be left: %v", pathNames(ps))
		}
		st, _ := s.GraphStats(ctx, "c")
		if st.FactSets != 0 || st.ManualEntities != 0 || st.ManualRelations != 0 || st.Entities != 2 || st.Relations != 1 {
			t.Fatalf("stats = %+v", st)
		}
		// and once the document goes the un-marked entities are collected like any extracted one
		if err := s.DeleteDocumentGraph(ctx, "c", "d1"); err != nil {
			t.Fatal(err)
		}
		if st, _ := s.GraphStats(ctx, "c"); st.Entities != 0 {
			t.Fatalf("orphans left: %+v", st)
		}
	})

	run("factset: an entity two fact sets declare stays curated until both are gone", func(t *testing.T, s store.Store) {
		a := store.FactSet{Collection: "c", ID: "a", Entities: []store.FactEntity{{Name: "Basalt", Type: "team"}, {Name: "Orion", Type: "service"}},
			Relations: []store.FactRelation{{Source: "Basalt", Predicate: "owns", Target: "Orion"}}}
		b := store.FactSet{Collection: "c", ID: "b", Entities: []store.FactEntity{{Name: "Team Basalt", Type: "team"}, {Name: "Nadia Rao", Type: "person"}},
			Relations: []store.FactRelation{{Source: "Nadia Rao", Predicate: "manages", Target: "Team Basalt"}}}
		for _, fs := range []store.FactSet{a, b} {
			if _, err := s.ReplaceFactSet(ctx, fs); err != nil {
				t.Fatal(err)
			}
		}
		if st, _ := s.GraphStats(ctx, "c"); st.Entities != 3 || st.Relations != 2 || st.FactSets != 2 {
			t.Fatalf("the shared entity must be one node: %+v", st)
		}
		if _, err := s.DeleteFactSet(ctx, "c", "a"); err != nil {
			t.Fatal(err)
		}
		e := findOne(t, ctx, s, "c", "Basalt")
		if e.Origin != store.OriginManual {
			t.Fatalf("still declared by fact set b, but %+v", e)
		}
		if hasEntity(ctx, s, "c", "Orion") {
			t.Fatal("Orion was only in fact set a")
		}
		if _, err := s.DeleteFactSet(ctx, "c", "b"); err != nil {
			t.Fatal(err)
		}
		if st, _ := s.GraphStats(ctx, "c"); st.Entities != 0 || st.Relations != 0 {
			t.Fatalf("stats = %+v", st)
		}
	})

	run("factset: same id in two collections are independent; an invalid set stores nothing", func(t *testing.T, s store.Store) {
		if _, err := s.ReplaceFactSet(ctx, catalogue("c1", "org")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReplaceFactSet(ctx, catalogue("c2", "org")); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.DeleteFactSet(ctx, "c1", "org"); !ok {
			t.Fatal("delete c1")
		}
		if infos, _ := s.ListFactSets(ctx, "c2"); len(infos) != 1 {
			t.Fatalf("c2 lost its fact set: %+v", infos)
		}
		bad := catalogue("c3", "org")
		bad.Relations = append(bad.Relations, store.FactRelation{Source: "Basalt", Predicate: "owns", Target: "Undeclared"})
		if _, err := s.ReplaceFactSet(ctx, bad); err == nil {
			t.Fatal("a relation to an undeclared entity was accepted")
		}
		if st, _ := s.GraphStats(ctx, "c3"); st.Entities != 0 || st.Relations != 0 || st.FactSets != 0 {
			t.Fatalf("a failed replace left rows behind: %+v", st)
		}
		// a failed replace of an existing set keeps the old one
		bad2 := catalogue("c2", "org")
		bad2.Relations = append(bad2.Relations, store.FactRelation{Source: "Nobody", Predicate: "owns", Target: "Orion"})
		if _, err := s.ReplaceFactSet(ctx, bad2); err == nil {
			t.Fatal("accepted")
		}
		if got, err := s.GetFactSet(ctx, "c2", "org"); err != nil || len(got.Relations) != 2 {
			t.Fatalf("old fact set not kept: %+v %v", got, err)
		}
	})
}
