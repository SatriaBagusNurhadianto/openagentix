package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/kb/internal/graph"
	"github.com/openagentix/openagentix/services/kb/internal/store"
)

func catalogue() store.FactSet {
	return store.FactSet{
		SourceURI: "https://example.test/catalogue",
		Entities: []store.FactEntity{
			{Name: "Basalt", Type: "team"}, {Name: "Priya Venkat", Type: "person"}, {Name: "Orion", Type: "service"},
		},
		Relations: []store.FactRelation{
			{Source: "Basalt", Predicate: "escalates_to", Target: "Priya Venkat", Note: "Escalations go to Priya Venkat"},
		},
	}
}

func TestPutFactSetValidatesAndStores(t *testing.T) {
	s := graphSvc()
	ctx := context.Background()
	res, err := s.PutFactSet(ctx, "kg", "org", catalogue())
	if err != nil || !res.Changed || res.Entities != 3 || res.Relations != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = s.PutFactSet(ctx, "kg", "org", catalogue())
	if err != nil || res.Changed {
		t.Fatalf("same body twice must change nothing: %+v %v", res, err)
	}
	got, err := s.GetFactSet(ctx, "kg", "org")
	if err != nil || got.SourceURI != "https://example.test/catalogue" || len(got.Entities) != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	infos, err := s.ListFactSets(ctx, "kg")
	if err != nil || len(infos) != 1 || infos[0].ID != "org" {
		t.Fatalf("%+v %v", infos, err)
	}
	if ok, err := s.DeleteFactSet(ctx, "kg", "org"); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if _, err := s.GetFactSet(ctx, "kg", "org"); !errors.Is(err, store.ErrFactSetNotFound) {
		t.Fatalf("%v", err)
	}
	if ok, err := s.DeleteFactSet(ctx, "kg", "org"); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestPutFactSetRejectsBadInput(t *testing.T) {
	s := graphSvc()
	ctx := context.Background()
	bad := catalogue()
	bad.Entities[1].Type = "gadget"
	badURI := catalogue()
	badURI.SourceURI = "javascript:alert(1)"
	tooBig := store.FactSet{}
	for i := 0; i <= graph.MaxFactEntities; i++ {
		tooBig.Entities = append(tooBig.Entities, store.FactEntity{Name: fmt.Sprint("S", i), Type: "service"})
	}
	for name, c := range map[string]struct {
		coll, id string
		fs       store.FactSet
		is       error
		text     string
	}{
		"bad type":       {"kg", "org", bad, ErrInvalid, "entities[1]: type"},
		"bad uri":        {"kg", "org", badURI, ErrInvalid, "source_uri"},
		"bad collection": {"no good", "org", catalogue(), ErrInvalid, "collection"},
		"bad id":         {"kg", "no good", catalogue(), ErrInvalid, "id"},
		"too big":        {"kg", "org", tooBig, ErrTooLarge, "2000"},
	} {
		_, err := s.PutFactSet(ctx, c.coll, c.id, c.fs)
		if !errors.Is(err, c.is) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if infos, _ := s.ListFactSets(ctx, "kg"); len(infos) != 0 {
		t.Fatalf("a rejected fact set was stored: %+v", infos)
	}
	off := graphSvc()
	off.Graph = nil
	if _, err := off.PutFactSet(ctx, "kg", "org", catalogue()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("graph disabled: %v", err)
	}
}

// A curated fact and extracted facts answer one question; the curated fact is
// marked as such, counts as one source and has no passage to quote.
func TestGraphSearchMixesCuratedAndExtractedFacts(t *testing.T) {
	s := graphSvc()
	ctx := context.Background()
	if _, err := s.Put(ctx, "kg", "own", DocInput{Text: "Service Orion is owned by team Basalt."}); err != nil {
		t.Fatal(err)
	}
	b, err := s.Store.UpsertEntity(ctx, store.EntityInput{Collection: "kg", Name: "Team Basalt", Type: "team"})
	if err != nil {
		t.Fatal(err)
	}
	o, _ := s.Store.UpsertEntity(ctx, store.EntityInput{Collection: "kg", Name: "Orion", Type: "service"})
	if _, err := s.Store.AddRelation(ctx, store.Relation{Collection: "kg", SrcID: b.ID, DstID: o.ID, Predicate: "owns", Evidence: "Service Orion is owned by team Basalt.", DocumentID: "own", ChunkIndex: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutFactSet(ctx, "kg", "org", catalogue()); err != nil {
		t.Fatal(err)
	}
	res, err := s.GraphSearch(ctx, "Who does the team that owns Orion escalate to?", "kg", 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	var found *GraphPath
	for i, p := range res.Paths {
		if chainHit(p.Entities, "orion", "basalt", "priya venkat") {
			found = &res.Paths[i]
		}
	}
	if found == nil {
		t.Fatalf("no Orion - Basalt - Priya Venkat chain: %+v", res.Paths)
	}
	var cur, doc int
	for _, e := range found.Edges {
		switch {
		case e.Curated:
			cur++
			if e.FactsetID != "org" || e.DocumentID != "" || e.SourceURI != "https://example.test/catalogue" || e.Evidence != "Escalations go to Priya Venkat" || e.Predicate != "escalates_to" {
				t.Fatalf("curated edge = %+v", e)
			}
		default:
			doc++
			if e.DocumentID != "own" || e.FactsetID != "" {
				t.Fatalf("document edge = %+v", e)
			}
		}
	}
	if cur != 1 || doc != 1 {
		t.Fatalf("edges: %d curated, %d from documents", cur, doc)
	}
	for _, c := range res.Chunks {
		if c.DocumentID == "" {
			t.Fatalf("a curated edge has no passage: %+v", c)
		}
	}
	if len(res.Chunks) != 1 {
		t.Fatalf("chunks = %+v", res.Chunks)
	}
	e, err := s.GraphEntity(ctx, "Basalt", "kg")
	if err != nil || !e.Found || len(e.Neighbours) != 2 {
		t.Fatalf("%+v %v", e, err)
	}
	p, err := s.GraphPaths(ctx, "Orion", "Priya Venkat", "kg", 3)
	if err != nil || len(p.Paths) != 1 || len(p.Chunks) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestChainsCountAFactSetAsOneSource(t *testing.T) {
	ent := func(id int64) store.Entity { return store.Entity{ID: id} }
	step := func(id int64, cite store.Citation) store.Step {
		return store.Step{Edge: store.Relation{ID: id}, Cite: cite}
	}
	two := cand{Coll: "c", Path: store.Path{Entities: []store.Entity{ent(1), ent(2), ent(3)}, Steps: []store.Step{
		step(1, store.Citation{FactsetID: "org"}), step(2, store.Citation{FactsetID: "org"})}}}
	if two.documents() != 1 {
		t.Fatalf("two edges of one fact set are one source, got %d", two.documents())
	}
	mixed := cand{Coll: "c", Path: store.Path{Entities: []store.Entity{ent(1), ent(2), ent(3)}, Steps: []store.Step{
		step(1, store.Citation{FactsetID: "org"}), step(2, store.Citation{DocumentID: "org"})}}}
	if mixed.documents() != 2 {
		t.Fatalf("a document that happens to be called like a fact set is another source, got %d", mixed.documents())
	}
}
