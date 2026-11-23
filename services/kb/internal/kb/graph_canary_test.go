package kb

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/kb/internal/chunk"
	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/graph"
	"github.com/openagentix/openagentix/services/kb/internal/store"
	"github.com/openagentix/openagentix/services/kb/internal/store/memory"
)

// canaryColl is the collection the real-model canary run used.
const canaryColl = "graph-canary"

// loadCanary stores the graph a real extraction model produced for the canary
// corpus (testdata/canary-graph-v1.txt, fictional content: lines "E|id|type|name"
// and "R|src|predicate|dst|document|evidence"). Every entity goes through the
// store's own merge, so the resulting graph is whatever the store makes of the
// model's names.
func loadCanary(t *testing.T) *Service {
	t.Helper()
	f, err := os.Open("testdata/canary-graph-v1.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s := &Service{
		Store: memory.New(0), Embedder: embed.None{}, Chunk: chunk.Options{MaxChars: 100000},
		Graph: &GraphOptions{WindowChars: 100000, PromptVersion: "v1"},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()
	type rel struct {
		src, dst  string
		pred, doc string
		evidence  string
	}
	var rels []rel
	docs := map[string][]string{}
	type ent struct{ typ, name string }
	ents := map[string]ent{}
	var order []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.SplitN(sc.Text(), "|", 6)
		switch p[0] {
		case "E":
			ents[p[1]] = ent{p[2], p[3]}
			order = append(order, p[1])
		case "R":
			rels = append(rels, rel{p[1], p[3], p[2], p[4], p[5]})
			docs[p[4]] = append(docs[p[4]], p[5])
		}
	}
	for id, lines := range docs {
		if _, err := s.Put(ctx, canaryColl, id, DocInput{Text: strings.Join(lines, "\n")}); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]int64{}
	for _, k := range order {
		e, err := s.Store.UpsertEntity(ctx, store.EntityInput{Collection: canaryColl, Name: ents[k].name, Type: ents[k].typ})
		if err != nil {
			t.Fatal(err)
		}
		ids[k] = e.ID
	}
	for _, r := range rels {
		if _, err := s.Store.AddRelation(ctx, store.Relation{
			Collection: canaryColl, SrcID: ids[r.src], DstID: ids[r.dst], Predicate: r.pred,
			Evidence: r.evidence, DocumentID: r.doc, ChunkIndex: 0, PromptVersion: "v1", Model: "real",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// chainHit reports whether a path names the wanted entities one by one, in order
// or reversed; a displayed name may carry a category word such as "cluster".
func chainHit(entities []string, want ...string) bool {
	if len(entities) != len(want) {
		return false
	}
	match := func(es []string) bool {
		for i, e := range es {
			if !strings.Contains(strings.ToLower(e), want[i]) {
				return false
			}
		}
		return true
	}
	rev := slices.Clone(entities)
	slices.Reverse(rev)
	return match(entities) || match(rev)
}

const canaryQuestion = "Who is on call for the team that owns the service whose backend suffered the Vane-3 outage?"

func TestCanaryQuestionReachesOrionThroughTheMergedGraph(t *testing.T) {
	s := loadCanary(t)
	res, err := s.GraphSearch(context.Background(), canaryQuestion, canaryColl, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Paths {
		if chainHit(p.Entities, "vane-3", "teal", "quill", "orion") {
			return
		}
	}
	for _, p := range res.Paths {
		t.Log(strings.Join(p.Entities, " > "))
	}
	t.Fatalf("no returned chain is Vane-3 <- Teal <- Quill <- Orion (%d chains, seeds %v)", len(res.Paths), res.Seeds)
}

func TestCanaryOrionToMiraOkafor(t *testing.T) {
	s := loadCanary(t)
	res, err := s.GraphPaths(context.Background(), "Orion", "Mira Okafor", canaryColl, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Paths) == 0 {
		t.Fatalf("no path from Orion to Mira Okafor: %+v", res)
	}
	p := res.Paths[0]
	if !chainHit(p.Entities, "orion", "basalt", "mira okafor") {
		t.Fatalf("path = %v", p.Entities)
	}
	var docs []string
	for _, e := range p.Edges {
		docs = append(docs, e.DocumentID)
	}
	slices.Sort(docs)
	if strings.Join(docs, ",") != "basalt-on-call,orion-ownership" {
		t.Fatalf("cited documents = %v", docs)
	}
}

func TestCanaryDistinctNamesStayDistinct(t *testing.T) {
	s := loadCanary(t)
	ctx := context.Background()
	for _, name := range []string{"Mira Okafor", "Mira Lindqvist", "Vane-1", "Vane-2", "Vane-3", "Quill", "Quire"} {
		hits, err := s.Store.FindEntitiesByName(ctx, canaryColl, name, 5)
		if err != nil || len(hits) == 0 || hits[0].Score != 1 {
			t.Fatalf("%s: %+v %v", name, hits, err)
		}
		if n := strings.ToLower(hits[0].Entity.Name); !strings.Contains(n, strings.ToLower(name)) {
			t.Errorf("%s resolved to %q", name, hits[0].Entity.Name)
		}
	}
}

// loadCanaryV2 replays the same model output through what the current code does
// with it: for every document of the canary corpus the entities and relations the
// real model produced for it are validated against the document's real text
// (graph.Validate) and stored (ApplyExtraction), as the extraction worker would.
func loadCanaryV2(t *testing.T) (*Service, map[string]int) {
	t.Helper()
	f, err := os.Open("testdata/canary-graph-v1.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type ent struct{ typ, name string }
	ents := map[string]ent{}
	type rel struct{ src, pred, dst, evidence string }
	byDoc := map[string][]rel{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.SplitN(sc.Text(), "|", 6)
		switch p[0] {
		case "E":
			ents[p[1]] = ent{p[2], p[3]}
		case "R":
			byDoc[p[4]] = append(byDoc[p[4]], rel{p[1], p[2], p[3], p[5]})
		}
	}
	s := &Service{
		Store: memory.New(0), Embedder: embed.None{}, Chunk: chunk.Options{MaxChars: 100000},
		Graph: &GraphOptions{WindowChars: 100000, PromptVersion: "v2"},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx := context.Background()
	counts := map[string]int{"in": 0, "kept": 0}
	for _, doc := range slices.Sorted(maps.Keys(byDoc)) {
		text, err := os.ReadFile("../../../../components/knowledge/graph-sample/docs/" + doc + ".md")
		if err != nil {
			t.Skipf("canary corpus not available: %v", err)
		}
		if _, err := s.Put(ctx, canaryColl, doc, DocInput{Text: string(text)}); err != nil {
			t.Fatal(err)
		}
		chunks, err := s.Store.DocumentChunks(ctx, canaryColl, doc)
		if err != nil {
			t.Fatal(err)
		}
		w := graph.Plan(chunks, 100000)[0]
		var es, rs []map[string]any
		seen := map[string]bool{}
		add := func(id string) string {
			e := ents[id]
			if !seen[id] {
				seen[id] = true
				es = append(es, map[string]any{"name": e.name, "type": e.typ, "description": "", "aliases": []string{}})
			}
			return e.name
		}
		for _, r := range byDoc[doc] {
			rs = append(rs, map[string]any{"source": add(r.src), "predicate": r.pred, "target": add(r.dst), "evidence": r.evidence})
		}
		out, _ := json.Marshal(map[string]any{"entities": es, "relations": rs})
		ex, st, err := graph.Validate(out, w, graph.Limits{MaxEntities: 100, MaxRelations: 100})
		if err != nil {
			t.Fatal(err)
		}
		counts["in"] += st.EntitiesIn
		counts["kept"] += len(ex.Entities)
		if err := s.Store.ApplyExtraction(ctx, store.ExtractionResult{
			Job: store.JobKey{Collection: canaryColl, DocumentID: doc, WindowIdx: 0}, Model: "real", PromptVersion: "v2",
			Entities: ex.Entities, Relations: ex.Relations,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return s, counts
}

func TestCanaryV2GenericNodesAreGoneAndTheChainsStay(t *testing.T) {
	s, counts := loadCanaryV2(t)
	ctx := context.Background()
	for _, generic := range []string{"database", "wiki", "dashboard", "provider", "on-call engineer", "disk outage", "network outage",
		"maintenance ticket", "storage layer", "internal wiki", "product database"} {
		hits, _ := s.Store.FindEntitiesByName(ctx, canaryColl, generic, 5)
		for _, h := range hits {
			if h.Score == 1 {
				t.Errorf("generic entity %q was stored as %q", generic, h.Entity.Name)
			}
		}
	}
	t.Logf("entities validated: %d in, %d kept", counts["in"], counts["kept"])
	res, err := s.GraphSearch(ctx, canaryQuestion, canaryColl, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i, p := range res.Paths {
		t.Logf("%d: %s", i, strings.Join(p.Entities, " > "))
		found = found || chainHit(p.Entities, "vane-3", "teal", "quill", "orion")
	}
	if !found {
		t.Fatalf("Vane-3 <- Teal <- Quill <- Orion is not among the %d chains returned", len(res.Paths))
	}
	pr, err := s.GraphPaths(ctx, "Orion", "Mira Okafor", canaryColl, 3)
	if err != nil || len(pr.Paths) == 0 || !chainHit(pr.Paths[0].Entities, "orion", "basalt", "mira okafor") {
		t.Fatalf("Orion -> Mira Okafor: %+v %v", pr, err)
	}
	st, _ := s.Store.GraphStats(ctx, canaryColl)
	t.Logf("graph: %d entities, %d relations", st.Entities, st.Relations)
}
