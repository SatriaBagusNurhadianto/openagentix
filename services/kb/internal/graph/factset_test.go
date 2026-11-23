package graph

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/kb/internal/store"
)

func okSet() store.FactSet {
	return store.FactSet{
		Collection: "c", ID: "org", SourceURI: "https://example.test/cat",
		Entities: []store.FactEntity{
			{Name: "  Team   Basalt ", Type: "Team", Description: "Owns  Orion", Aliases: []string{"basalt squad", "Basalt Squad", "Team Basalt"}},
			{Name: "Orion", Type: "service"},
			{Name: "Nadia Rao", Type: "person"},
		},
		Relations: []store.FactRelation{
			{Source: "Basalt", Predicate: "Owns", Target: "orion", Note: "  Basalt   owns Orion "},
			{Source: "basalt squad", Predicate: "managed by", Target: "Nadia Rao"},
		},
	}
}

func TestValidateFactSetNormalisesAndResolves(t *testing.T) {
	got, err := ValidateFactSet(okSet())
	if err != nil {
		t.Fatal(err)
	}
	e := got.Entities[0]
	if e.Name != "Team Basalt" || e.Type != "team" || e.Description != "Owns Orion" || len(e.Aliases) != 1 || e.Aliases[0] != "basalt squad" {
		t.Fatalf("entity = %+v", e)
	}
	r := got.Relations
	// endpoints are rewritten to the declared names; predicates are snake_case; notes are one line
	if r[0] != (store.FactRelation{Source: "Team Basalt", Predicate: "owns", Target: "Orion", Note: "Basalt owns Orion"}) {
		t.Fatalf("relation 0 = %+v", r[0])
	}
	if r[1].Source != "Team Basalt" || r[1].Predicate != "managed_by" || r[1].Target != "Nadia Rao" {
		t.Fatalf("relation 1 (by alias) = %+v", r[1])
	}
	if got.SourceURI != "https://example.test/cat" {
		t.Fatalf("source = %q", got.SourceURI)
	}
	if _, err := got.Resolve(); err != nil {
		t.Fatalf("a validated set must resolve in the store: %v", err)
	}
}

func TestValidateFactSetDoesNotApplyExtractionOnlyRules(t *testing.T) {
	// The operator decides: a generic name, a person without a capital, an event
	// without a digit are all fine in a curated catalogue.
	fs := store.FactSet{Entities: []store.FactEntity{
		{Name: "database", Type: "system"}, {Name: "on-call engineer", Type: "person"}, {Name: "disk outage", Type: "event"},
	}, Relations: []store.FactRelation{{Source: "on-call engineer", Predicate: "owns", Target: "database"}}}
	if _, err := ValidateFactSet(fs); err != nil {
		t.Fatalf("manual entities must not be judged like extracted ones: %v", err)
	}
}

func TestValidateFactSetRejectsWithTheIndexOfTheItem(t *testing.T) {
	long := strings.Repeat("x", MaxNameRunes+1)
	cases := []struct {
		name string
		edit func(*store.FactSet)
		want string
	}{
		{"unknown type", func(f *store.FactSet) { f.Entities[1].Type = "gadget" }, "entities[1]: type"},
		{"empty type", func(f *store.FactSet) { f.Entities[1].Type = "" }, "entities[1]: type"},
		{"empty name", func(f *store.FactSet) { f.Entities[2].Name = "  " }, "entities[2]: name"},
		{"long name", func(f *store.FactSet) { f.Entities[2].Name = long }, "entities[2]: name"},
		{"control char", func(f *store.FactSet) { f.Entities[0].Name = "a\x00b" }, "entities[0]: name"},
		{"invalid utf8", func(f *store.FactSet) { f.Entities[0].Name = "a\xffb" }, "entities[0]: name"},
		{"long description", func(f *store.FactSet) { f.Entities[1].Description = strings.Repeat("d", MaxDescriptionRunes+1) }, "entities[1]: description"},
		{"too many aliases", func(f *store.FactSet) {
			for i := 0; i <= MaxFactAliases; i++ {
				f.Entities[1].Aliases = append(f.Entities[1].Aliases, fmt.Sprintf("alias %d", i))
			}
		}, "entities[1]: aliases"},
		{"bad alias", func(f *store.FactSet) { f.Entities[1].Aliases = []string{long} }, "entities[1]: alias"},
		{"duplicate entity", func(f *store.FactSet) { f.Entities[2] = store.FactEntity{Name: "ORION service", Type: "system"} }, "entities[2]: same entity as entities[1]"},
		{"bad predicate", func(f *store.FactSet) { f.Relations[1].Predicate = "über" }, "relations[1]: predicate"},
		{"empty predicate", func(f *store.FactSet) { f.Relations[0].Predicate = "" }, "relations[0]: predicate"},
		{"unknown source", func(f *store.FactSet) { f.Relations[1].Source = "Nobody" }, "relations[1]: source is not an entity declared in this fact set"},
		{"unknown target", func(f *store.FactSet) { f.Relations[0].Target = "Nowhere" }, "relations[0]: target is not an entity declared in this fact set"},
		{"self relation", func(f *store.FactSet) { f.Relations[0].Target = "Basalt" }, "relations[0]: source and target are the same entity"},
		{"long note", func(f *store.FactSet) { f.Relations[0].Note = strings.Repeat("n", MaxEvidenceRunes+1) }, "relations[0]: note"},
		{"ambiguous endpoint", func(f *store.FactSet) {
			f.Entities[0].Aliases = []string{"bsl"}
			f.Entities[1].Aliases = []string{"bsl"}
			f.Relations[1].Source = "bsl"
		}, "relations[1]: source is ambiguous"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := okSet()
			c.edit(&fs)
			_, err := ValidateFactSet(fs)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
			if errors.Is(err, ErrFactSetTooLarge) {
				t.Fatalf("not a size error: %v", err)
			}
			if strings.Contains(err.Error(), "Nobody") || strings.Contains(err.Error(), long) {
				t.Fatalf("the error echoes content: %v", err)
			}
		})
	}
}

func TestValidateFactSetBoundsItsSize(t *testing.T) {
	fs := store.FactSet{}
	for i := 0; i <= MaxFactEntities; i++ {
		fs.Entities = append(fs.Entities, store.FactEntity{Name: fmt.Sprintf("Service %d", i), Type: "service"})
	}
	if _, err := ValidateFactSet(fs); !errors.Is(err, ErrFactSetTooLarge) || !strings.Contains(err.Error(), "2000") {
		t.Fatalf("entities: %v", err)
	}
	fs = store.FactSet{Entities: []store.FactEntity{{Name: "A1", Type: "service"}, {Name: "B1", Type: "service"}}}
	for i := 0; i <= MaxFactRelations; i++ {
		fs.Relations = append(fs.Relations, store.FactRelation{Source: "A1", Predicate: "p" + fmt.Sprint(i), Target: "B1"})
	}
	if _, err := ValidateFactSet(fs); !errors.Is(err, ErrFactSetTooLarge) || !strings.Contains(err.Error(), "5000") {
		t.Fatalf("relations: %v", err)
	}
	if _, err := ValidateFactSet(store.FactSet{}); err != nil {
		t.Fatalf("an empty fact set clears the previous one and is valid: %v", err)
	}
}

func TestValidateFactSetDropsRepeatedRelations(t *testing.T) {
	fs := okSet()
	fs.Relations = append(fs.Relations, store.FactRelation{Source: "Team Basalt", Predicate: "OWNS", Target: "Orion", Note: "again"})
	got, err := ValidateFactSet(fs)
	if err != nil || len(got.Relations) != 2 || got.Relations[0].Note != "Basalt owns Orion" {
		t.Fatalf("%+v %v", got.Relations, err)
	}
}

func TestValidateFactSetIsIdempotent(t *testing.T) {
	a, err := ValidateFactSet(okSet())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ValidateFactSet(a)
	if err != nil || a.Hash() != b.Hash() {
		t.Fatalf("validating a validated set changed it: %v", err)
	}
}
