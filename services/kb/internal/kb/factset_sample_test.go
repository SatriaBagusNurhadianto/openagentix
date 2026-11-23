package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/kb/internal/graph"
	"github.com/openagentix/openagentix/services/kb/internal/store"
)

const sampleCatalogue = "../../../../components/knowledge/graph-sample/org-catalogue.yaml"

// loadSampleCatalogue converts the sample YAML to JSON with yq, as
// hack/kb-graph-load.sh does, and decodes it as the API does. It skips when yq
// is not installed.
func loadSampleCatalogue(t *testing.T) store.FactSet {
	t.Helper()
	if _, err := exec.LookPath("yq"); err != nil {
		t.Skip("yq is not installed")
	}
	var out []byte
	var err error
	for _, args := range [][]string{{"-c", ".", sampleCatalogue}, {"-o=json", "-I=0", ".", sampleCatalogue}} { // python yq, then Go yq
		if out, err = exec.Command("yq", args...).Output(); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("yq: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	var fs store.FactSet
	if err := dec.Decode(&fs); err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestSampleCatalogueIsValidAndStatesNoLinkOfTheCanaryChain(t *testing.T) {
	fs := loadSampleCatalogue(t)
	v, err := graph.ValidateFactSet(fs)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Entities) != len(fs.Entities) || len(v.Relations) != len(fs.Relations) {
		t.Fatalf("validation dropped something: %d/%d entities, %d/%d relations", len(v.Entities), len(fs.Entities), len(v.Relations), len(fs.Relations))
	}
	// The five links of the canary chain (README of the sample corpus) must stay in the documents.
	for _, r := range v.Relations {
		pair := strings.ToLower(r.Source + " " + r.Target)
		for _, chain := range []string{"basalt orion", "orion basalt", "orion quill", "quill teal", "basalt mira okafor", "teal vane-3"} {
			if pair == chain {
				t.Errorf("the catalogue states a link of the canary chain: %+v", r)
			}
		}
		if strings.Contains(strings.ToLower(r.Source+r.Target), "mira") || strings.Contains(strings.ToLower(r.Source+r.Target), "vane") || strings.Contains(strings.ToLower(r.Source+r.Target), "teal") {
			t.Errorf("the catalogue mentions the canary's answer: %+v", r)
		}
	}
}

// The question of the live check needs one curated fact and two extracted ones.
const mixedQuestion = "Which service calls the ledger API Quill, which team owns it, and who is that team's escalation manager?"

func TestSampleCatalogueAnswersTheMixedQuestionTogetherWithTheCanaryGraph(t *testing.T) {
	fs := loadSampleCatalogue(t)
	s := loadCanary(t)
	ctx := context.Background()
	without, err := s.GraphSearch(ctx, mixedQuestion, canaryColl, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range without.Paths {
		if strings.Contains(strings.Join(p.Entities, ">"), "Priya Venkat") {
			t.Fatalf("the corpus alone must not know the manager: %v", p.Entities)
		}
	}
	if _, err := s.PutFactSet(ctx, canaryColl, "org-catalogue", fs); err != nil {
		t.Fatal(err)
	}
	res, err := s.GraphSearch(ctx, mixedQuestion, canaryColl, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	var docs, curated int
	found := false
	for _, p := range res.Paths {
		if chainHit(p.Entities, "quill", "orion", "basalt", "priya venkat") {
			found = true
			for _, e := range p.Edges {
				if e.Curated {
					curated++
				} else {
					docs++
				}
			}
		}
	}
	if !found || curated != 1 || docs != 2 {
		for _, p := range res.Paths {
			t.Log(strings.Join(p.Entities, " > "))
		}
		t.Fatalf("want Quill - Orion - Basalt - Priya Venkat with two extracted facts and one curated; found=%v curated=%d docs=%d", found, curated, docs)
	}
	// The distractor ledger API keeps its own manager.
	other, err := s.GraphSearch(ctx, "Which team owns the service that calls the ledger API Quire, and who is that team's escalation manager?", canaryColl, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	for _, p := range other.Paths {
		hit = hit || chainHit(p.Entities, "quire", "sparrow", "granite", "hana mori")
		if strings.Contains(strings.Join(p.Entities, ">"), "Priya Venkat") {
			t.Fatalf("a chain through the wrong team: %v", p.Entities)
		}
	}
	if !hit {
		t.Fatalf("Quire - Sparrow - Granite - Hana Mori not found: %+v", other.Paths)
	}
}

// The curated facts must not disturb the canary chain.
func TestCanaryStillPassesWithTheCatalogueLoaded(t *testing.T) {
	fs := loadSampleCatalogue(t)
	s := loadCanary(t)
	if _, err := s.PutFactSet(context.Background(), canaryColl, "org-catalogue", fs); err != nil {
		t.Fatal(err)
	}
	res, err := s.GraphSearch(context.Background(), canaryQuestion, canaryColl, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	ok := false
	for _, p := range res.Paths {
		ok = ok || chainHit(p.Entities, "vane-3", "teal", "quill", "orion")
	}
	if !ok {
		t.Fatalf("the canary chain is lost: %+v", res.Paths)
	}
	paths, err := s.GraphPaths(context.Background(), "Orion", "Mira Okafor", canaryColl, 3)
	if err != nil || len(paths.Paths) == 0 || !chainHit(paths.Paths[0].Entities, "orion", "basalt", "mira okafor") {
		t.Fatalf("%+v %v", paths, err)
	}
}
