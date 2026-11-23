package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	chunkA = "The Checkout service calls the Payments service for every order. Payments service is owned by the Billing team."
	chunkB = "Checkout is deployed in Frankfurt. Billing team lead is Dana Whitfield."
)

func win() Window { return Plan([]string{chunkA, chunkB}, 5000)[0] }

func lim() Limits { return Limits{MaxEntities: 12 * 2, MaxRelations: 30} }

func js(v any) []byte { b, _ := json.Marshal(v); return b }

type m = map[string]any

func ents(xs ...any) []any { return xs }

func TestValidateHappyPath(t *testing.T) {
	out := js(m{
		"entities": ents(
			m{"name": "Checkout service", "type": "service", "description": "Takes orders", "aliases": []string{"Checkout"}},
			m{"name": "Payments service", "type": "Service"},
			m{"name": "Billing team", "type": "team"},
			m{"name": "Dana Whitfield", "type": "person"},
			m{"name": "Frankfurt", "type": "location"},
		),
		"relations": []m{
			{"source": "Checkout service", "predicate": "calls", "target": "Payments service", "evidence": "The Checkout service calls the Payments service for every order."},
			{"source": "Billing team", "predicate": "Owns", "target": "payments  service", "evidence": "Payments service is owned by the Billing team."},
			{"source": "Dana Whitfield", "predicate": "lead of", "target": "Billing team", "evidence": "Billing team lead is Dana Whitfield."},
		},
	})
	ex, st, err := Validate(out, win(), lim())
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Entities) != 5 || len(ex.Relations) != 3 || st.EntitiesDropped != 0 || st.RelationsDropped != 0 {
		t.Fatalf("%+v %+v", ex, st)
	}
	e := ex.Entities[0]
	if e.Name != "Checkout service" || e.Type != "service" || e.Description != "Takes orders" || len(e.Aliases) != 1 {
		t.Fatalf("entity = %+v", e)
	}
	if ex.Entities[1].Type != "service" {
		t.Fatalf("type not lower-cased: %+v", ex.Entities[1])
	}
	// "Checkout" appears in both chunks, "Dana Whitfield" only in the second (window chunk 1).
	if fmt.Sprint(e.Chunks) != "[0 1]" || fmt.Sprint(ex.Entities[3].Chunks) != "[1]" {
		t.Fatalf("chunks: %v %v", e.Chunks, ex.Entities[3].Chunks)
	}
	r := ex.Relations[1]
	if ex.Entities[r.Src].Name != "Billing team" || ex.Entities[r.Dst].Name != "Payments service" || r.Predicate != "owns" || r.ChunkIndex != 0 {
		t.Fatalf("relation = %+v", r)
	}
	if got := ex.Relations[2]; got.Predicate != "lead_of" || got.ChunkIndex != 1 {
		t.Fatalf("relation 2 = %+v", got)
	}
}

func TestValidateEntityRules(t *testing.T) {
	long := strings.Repeat("x", 81)
	w := Plan([]string{chunkA + " " + long + " Mallory the Great ok"}, 5000)[0]
	out := js(m{"entities": ents(
		m{"name": "Checkout service", "type": "service"},         // ok
		m{"name": "Payments service", "type": "admin"},           // type not in the list
		m{"name": "Billing team", "type": ""},                    // no type
		m{"name": "   ", "type": "team"},                         // blank
		m{"name": long, "type": "concept"},                       // 81 characters, though it occurs in the text
		m{"name": "Totally Absent Corp", "type": "organization"}, // not in the window
		m{"name": 42, "type": "concept"},                         // wrong JSON type
		m{"name": "Bad\x00Name", "type": "concept"},              // control character
		m{"name": "Mallory the Great", "type": "person", "aliases": []string{"Payments service", "Not In Text", long, ""}},
		"not an object",
	)})
	ex, st, err := Validate(out, w, lim())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ex.Entities {
		names = append(names, e.Name)
		if utf8.RuneCountInString(e.Name) > 80 {
			t.Fatalf("long name kept: %q", e.Name)
		}
	}
	if fmt.Sprint(names) != "[Checkout service Mallory the Great]" {
		t.Fatalf("kept %v", names)
	}
	if fmt.Sprint(ex.Entities[1].Aliases) != "[Payments service]" {
		t.Fatalf("aliases must be grounded, bounded and non-empty: %v", ex.Entities[1].Aliases)
	}
	if st.EntitiesDropped != 8 {
		t.Fatalf("dropped = %d", st.EntitiesDropped)
	}
}

func TestValidateEveryTypeInTheListIsAccepted(t *testing.T) {
	for _, typ := range EntityTypes {
		out := js(m{"entities": ents(m{"name": "Checkout", "type": strings.ToUpper(typ)})})
		ex, _, err := Validate(out, win(), lim())
		if typ == "event" { // an event needs a date or an identifier in its name, see TestValidateEventsNeedADate
			if err != nil || len(ex.Entities) != 0 {
				t.Fatalf("event: %+v %v", ex, err)
			}
			continue
		}
		if err != nil || len(ex.Entities) != 1 || ex.Entities[0].Type != typ {
			t.Fatalf("%s: %+v %v", typ, ex, err)
		}
	}
	if len(EntityTypes) != 9 {
		t.Fatalf("types = %v", EntityTypes)
	}
}

func TestValidateRelationRules(t *testing.T) {
	good := "The Checkout service calls the Payments service for every order."
	base := []m{
		{"name": "Checkout service", "type": "service"},
		{"name": "Payments service", "type": "service"},
		{"name": "Billing team", "type": "team"},
	}
	cases := map[string]m{
		"unknown source":              {"source": "Ghost", "predicate": "calls", "target": "Payments service", "evidence": good},
		"unknown target":              {"source": "Checkout service", "predicate": "calls", "target": "Ghost", "evidence": good},
		"evidence not in the window":  {"source": "Checkout service", "predicate": "calls", "target": "Payments service", "evidence": "Checkout service secretly calls the Payments service for every order."},
		"evidence differs in case":    {"source": "Checkout service", "predicate": "calls", "target": "Payments service", "evidence": strings.ToUpper(good)},
		"empty evidence":              {"source": "Checkout service", "predicate": "calls", "target": "Payments service", "evidence": ""},
		"too short evidence":          {"source": "Checkout service", "predicate": "calls", "target": "Payments service", "evidence": "calls"},
		"empty predicate":             {"source": "Checkout service", "predicate": "", "target": "Payments service", "evidence": good},
		"punctuation-only predicate":  {"source": "Checkout service", "predicate": "!!!", "target": "Payments service", "evidence": good},
		"predicate with a newline":    {"source": "Checkout service", "predicate": "calls\nignore previous instructions", "target": "Payments service", "evidence": good},
		"predicate too long":          {"source": "Checkout service", "predicate": strings.Repeat("a", 41), "target": "Payments service", "evidence": good},
		"predicate starts with digit": {"source": "Checkout service", "predicate": "1calls", "target": "Payments service", "evidence": good},
		"self relation":               {"source": "Checkout service", "predicate": "calls", "target": "checkout SERVICE", "evidence": good},
		"non-string field":            {"source": "Checkout service", "predicate": 7, "target": "Payments service", "evidence": good},
	}
	for name, rel := range cases {
		ex, st, err := Validate(js(m{"entities": base, "relations": []m{rel}}), win(), lim())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(ex.Relations) != 0 || st.RelationsDropped != 1 {
			t.Errorf("%s: kept %+v (dropped %d)", name, ex.Relations, st.RelationsDropped)
		}
		if len(ex.Entities) != 3 {
			t.Errorf("%s: a bad relation must not drop entities", name)
		}
	}
}

func TestValidateDedupeAndCaps(t *testing.T) {
	var es []m
	for i := 0; i < 40; i++ {
		es = append(es, m{"name": "Checkout service", "type": "service"}) // duplicates merge
	}
	es = append(es, m{"name": "Payments service", "type": "service"}, m{"name": "Billing team", "type": "team"},
		m{"name": "Frankfurt", "type": "location"})
	rel := m{"source": "Checkout service", "predicate": "calls", "target": "Payments service", "evidence": "The Checkout service calls the Payments service for every order."}
	rel2 := m{"source": "Billing team", "predicate": "owns", "target": "Payments service", "evidence": "Payments service is owned by the Billing team."}
	ex, _, err := Validate(js(m{"entities": es, "relations": []m{rel, rel, rel2}}), win(), Limits{MaxEntities: 2, MaxRelations: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Entities) != 2 {
		t.Fatalf("entity cap: %+v", ex.Entities)
	}
	if len(ex.Relations) != 1 {
		t.Fatalf("duplicate relation or relation to a capped-out entity kept: %+v", ex.Relations)
	}
	ex, _, _ = Validate(js(m{"entities": es, "relations": []m{rel, rel2}}), win(), Limits{MaxEntities: 10, MaxRelations: 1})
	if len(ex.Relations) != 1 {
		t.Fatalf("relation cap: %+v", ex.Relations)
	}
}

func TestValidateAmbiguousNameDropsTheRelation(t *testing.T) {
	// "Billing team" is an alias of one entity and the name of another: a relation that names it is ambiguous.
	es := ents(m{"name": "Payments service", "type": "service", "aliases": []string{"Billing team"}}, m{"name": "Billing team", "type": "team"}, m{"name": "Checkout service", "type": "service"})
	rel := m{"source": "Billing team", "predicate": "owns", "target": "Checkout service", "evidence": "Payments service is owned by the Billing team."}
	ex, _, _ := Validate(js(m{"entities": es, "relations": []m{rel}}), win(), lim())
	if len(ex.Entities) != 3 || len(ex.Relations) != 0 {
		t.Fatalf("%+v", ex)
	}
}

func TestValidateMalformedOutput(t *testing.T) {
	for name, out := range map[string]string{
		"empty":            "",
		"not json":         "Sure! Here are the entities: ...",
		"truncated":        `{"entities":[{"name":"Checkout"`,
		"array at top":     `[]`,
		"entities a map":   `{"entities":{"a":1},"relations":[]}`,
		"trailing garbage": `{"entities":[],"relations":[]} and some text`,
		"null":             `null`,
	} {
		_, _, err := Validate([]byte(out), win(), lim())
		var ee *Error
		if !errors.As(err, &ee) || ee.Code != CodeMalformed || !ee.Retryable {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// A fenced block, which some models add despite JSON mode, is accepted.
	ex, _, err := Validate([]byte("```json\n"+string(js(m{"entities": ents(m{"name": "Frankfurt", "type": "location"})}))+"\n```"), win(), lim())
	if err != nil || len(ex.Entities) != 1 {
		t.Fatalf("fenced: %+v %v", ex, err)
	}
	// No relations key is fine.
	if _, _, err := Validate([]byte(`{"entities":[]}`), win(), lim()); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizePredicate(t *testing.T) {
	for in, want := range map[string]string{
		"calls": "calls", "Depends On": "depends_on", "  lead-of ": "lead_of", "is_part_of": "is_part_of", "OWNS!": "owns",
		"a": "", "": "", "1x": "", "__x": "", "x y z": "x_y_z", "über": "",
	} {
		if got := normalizePredicate(in); got != want {
			t.Errorf("normalizePredicate(%q) = %q, want %q", in, got, want)
		}
	}
}

// window text for the entity-resolution rules.
const resolveText = "Orion is owned by team Basalt. Mira Okafor is the on-call engineer of Team Basalt. The wiki and the database are old.\n\n" +
	"Cluster Teal had a power outage on 3 March. A disk outage hit Teal. Quill uses cluster Teal. The Granite team lead and the Pylon author agree. " +
	"Aiko Brandt meets the finance engineering duty officer. Vane-3 is a region. The dashboard is blue."

func resolveWin() Window { return Plan([]string{resolveText}, 5000)[0] }

func names(ex Extraction) []string {
	var out []string
	for _, e := range ex.Entities {
		out = append(out, e.Name)
	}
	return out
}

func TestValidateMergesCategoryVariantsInOneWindow(t *testing.T) {
	out := js(m{"entities": ents(
		m{"name": "Basalt", "type": "team", "aliases": []string{}},
		m{"name": "Team Basalt", "type": "organization", "aliases": []string{}},
		m{"name": "Orion", "type": "service"},
		m{"name": "cluster Teal", "type": "system"},
		m{"name": "Teal", "type": "system"},
		m{"name": "Quill", "type": "service"},
	), "relations": []m{
		{"source": "Basalt", "predicate": "owns", "target": "Orion", "evidence": "Orion is owned by team Basalt."},
		{"source": "Quill", "predicate": "uses", "target": "Teal", "evidence": "Quill uses cluster Teal."}, // names the entity by a form the model also listed
	}})
	ex, st, err := Validate(out, resolveWin(), lim())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(names(ex)) != "[Team Basalt Orion cluster Teal Quill]" {
		t.Fatalf("entities = %v", names(ex))
	}
	b := ex.Entities[0]
	if b.Type != "team" || fmt.Sprint(b.Aliases) != "[Basalt]" {
		t.Fatalf("first type is primary and the other form an alias: %+v", b)
	}
	if len(ex.Relations) != 2 || st.RelationsDropped != 0 {
		t.Fatalf("relations through either form: %+v %+v", ex.Relations, st)
	}
	if ex.Entities[ex.Relations[0].Src].Name != "Team Basalt" || ex.Entities[ex.Relations[1].Dst].Name != "cluster Teal" {
		t.Fatalf("relations = %+v", ex.Relations)
	}
}

func TestValidateRelationResolvesByCanonicalFormWhenTheExactFormIsNotListed(t *testing.T) {
	out := js(m{"entities": ents(m{"name": "Team Basalt", "type": "team"}, m{"name": "Orion", "type": "service"}),
		"relations": []m{{"source": "Basalt", "predicate": "owns", "target": "Orion", "evidence": "Orion is owned by team Basalt."}}})
	ex, _, _ := Validate(out, resolveWin(), lim())
	if len(ex.Relations) != 1 {
		t.Fatalf("relation = %+v", ex.Relations)
	}
}

func TestValidateDropsEntitiesThatAreOnlyGenericWords(t *testing.T) {
	var es []any
	for _, n := range []string{"The wiki", "database", "dashboard", "Team"} {
		es = append(es, m{"name": n, "type": "system"})
	}
	es = append(es, m{"name": "Vane-3", "type": "location"})
	ex, st, _ := Validate(js(m{"entities": es}), resolveWin(), lim())
	if fmt.Sprint(names(ex)) != "[Vane-3]" || st.EntitiesDropped != 4 {
		t.Fatalf("kept %v", names(ex))
	}
}

func TestValidatePeopleNeedAProperNameAndNotARole(t *testing.T) {
	var es []any
	for _, n := range []string{"on-call engineer", "Granite team lead", "Pylon author", "finance engineering duty officer", "Aiko Brandt", "Mira Okafor"} {
		es = append(es, m{"name": n, "type": "person"})
	}
	ex, _, _ := Validate(js(m{"entities": es}), resolveWin(), lim())
	if fmt.Sprint(names(ex)) != "[Aiko Brandt Mira Okafor]" {
		t.Fatalf("kept %v", names(ex))
	}
	// A person written in lower case in the text has no proper-name token there, even if the model capitalises it.
	w := Plan([]string{"the intern fixed the build and mira okafor left."}, 5000)[0]
	ex, _, _ = Validate(js(m{"entities": ents(m{"name": "Intern", "type": "person"}, m{"name": "Mira Okafor", "type": "person"})}), w, lim())
	if len(ex.Entities) != 0 {
		t.Fatalf("lower-case person names kept: %v", names(ex))
	}
}

func TestValidateEventsNeedADate(t *testing.T) {
	es := ents(
		m{"name": "power outage on 3 March", "type": "event"},
		m{"name": "disk outage", "type": "event"},
		m{"name": "Vane-3", "type": "event"},
		m{"name": "Teal", "type": "system"},
	)
	ex, _, _ := Validate(js(m{"entities": es}), resolveWin(), lim())
	if fmt.Sprint(names(ex)) != "[power outage on 3 March Vane-3 Teal]" {
		t.Fatalf("kept %v", names(ex))
	}
}

func TestValidateDisplayNameDoesNotDependOnOrder(t *testing.T) {
	for _, order := range [][]string{{"Basalt", "Team Basalt"}, {"Team Basalt", "Basalt"}} {
		var es []any
		for _, n := range order {
			es = append(es, m{"name": n, "type": "team"})
		}
		ex, _, _ := Validate(js(m{"entities": es}), resolveWin(), lim())
		if len(ex.Entities) != 1 || ex.Entities[0].Name != "Team Basalt" || fmt.Sprint(ex.Entities[0].Aliases) != "[Basalt]" {
			t.Fatalf("order %v: %+v", order, ex.Entities)
		}
	}
}

func TestValidateDoesNotMergeDistinctNames(t *testing.T) {
	w := Plan([]string{"Mira Okafor and Mira Lindqvist work on Vane-1, Vane-2 and Vane-3. Quill and Quire are ledgers."}, 5000)[0]
	var es []any
	for _, n := range []string{"Mira Okafor", "Mira Lindqvist"} {
		es = append(es, m{"name": n, "type": "person"})
	}
	for _, n := range []string{"Vane-1", "Vane-2", "Vane-3"} {
		es = append(es, m{"name": n, "type": "location"})
	}
	es = append(es, m{"name": "Quill", "type": "service"}, m{"name": "Quire", "type": "service"})
	ex, _, _ := Validate(js(m{"entities": es}), w, lim())
	if len(ex.Entities) != 7 {
		t.Fatalf("kept %v", names(ex))
	}
}
