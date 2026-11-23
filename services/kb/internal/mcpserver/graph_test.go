package mcpserver

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openagentix/openagentix/services/kb/internal/chunk"
	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/kb"
	"github.com/openagentix/openagentix/services/kb/internal/store"
	"github.com/openagentix/openagentix/services/kb/internal/store/memory"
)

// axisEmbedder maps a text to a unit axis by keyword, so tests can steer the
// query-to-entity embedding match.
type axisEmbedder struct{}

func (axisEmbedder) Dimensions() int { return 3 }
func (axisEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		switch {
		case strings.Contains(strings.ToLower(t), "ledger"):
			out[i] = []float32{1, 0, 0}
		case strings.Contains(strings.ToLower(t), "pager"):
			out[i] = []float32{0, 1, 0}
		default:
			out[i] = []float32{0, 0, 1}
		}
	}
	return out, nil
}

type genv struct {
	*env
	st store.Store
}

func setupGraph(t *testing.T, graphOn bool, emb embed.Embedder) *genv {
	t.Helper()
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	st := memory.New(emb.Dimensions())
	svc := &kb.Service{Store: st, Embedder: emb, Chunk: chunk.Options{MaxChars: 400}, DocMaxChars: 5000, Log: log}
	if graphOn {
		svc.Graph = &kb.GraphOptions{WindowChars: 2400, PromptVersion: "v1", MaxHops: 3}
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", New(svc, log, Options{Stateless: true, Version: "test", Timeout: 5 * time.Second}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &genv{env: &env{svc: svc, session: session, logs: logs}, st: st}
}

// seedChain stores the five-link chain of the canary in collection "kg", one
// fact per document, and returns the documents' ids.
func (g *genv) seedChain(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	docs := map[string]string{
		"d-owner":  "Service Orion is owned by team Basalt.",
		"d-calls":  "Orion calls the ledger API Quill.",
		"d-store":  "Quill stores its data in cluster Teal.",
		"d-oncall": "Team Basalt's on-call engineer is Mira Okafor.",
		"d-region": "Cluster Teal runs in region Vane-3 and had a disk outage on 3 March.",
	}
	for id, text := range docs {
		g.put(t, "kg", id, "Doc "+id, text)
	}
	ent := func(name, typ string) store.Entity {
		e, err := g.st.UpsertEntity(ctx, store.EntityInput{Collection: "kg", Name: name, Type: typ})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	rel := func(src, dst store.Entity, pred, doc, evidence string) {
		if _, err := g.st.AddRelation(ctx, store.Relation{Collection: "kg", SrcID: src.ID, DstID: dst.ID, Predicate: pred, Evidence: evidence, DocumentID: doc, PromptVersion: "v1", Model: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	orion, basalt, quill := ent("Orion", "service"), ent("Basalt", "team"), ent("Quill", "service")
	teal, vane, mira := ent("Teal", "system"), ent("Vane-3", "location"), ent("Mira Okafor", "person")
	rel(basalt, orion, "owns", "d-owner", "Service Orion is owned by team Basalt.")
	rel(orion, quill, "calls", "d-calls", "Orion calls the ledger API Quill.")
	rel(quill, teal, "stores_data_in", "d-store", "Quill stores its data in cluster Teal.")
	rel(basalt, mira, "has_on_call", "d-oncall", "Team Basalt's on-call engineer is Mira Okafor.")
	rel(teal, vane, "runs_in", "d-region", "Cluster Teal runs in region Vane-3")
}

func toolNames(t *testing.T, e *env) []string {
	t.Helper()
	res, err := e.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, tool := range res.Tools {
		n = append(n, tool.Name)
	}
	slices.Sort(n)
	return n
}

func TestGraphToolsOnlyWhenGraphEnabled(t *testing.T) {
	off := setupGraph(t, false, embed.None{})
	if got := toolNames(t, off.env); !slices.Equal(got, []string{"kb_get_document", "kb_list_collections", "kb_search"}) {
		t.Fatalf("graph off: tool list changed: %v", got)
	}
	on := setupGraph(t, true, embed.None{})
	want := []string{"kb_get_document", "kb_graph_entity", "kb_graph_paths", "kb_graph_search", "kb_list_collections", "kb_search"}
	if got := toolNames(t, on.env); !slices.Equal(got, want) {
		t.Fatalf("graph on: tools = %v", got)
	}
	res, _ := on.session.ListTools(context.Background(), nil)
	for _, tool := range res.Tools {
		if strings.HasPrefix(tool.Name, "kb_graph_") && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || len(tool.Description) < 40) {
			t.Errorf("%s: needs read-only annotation and a description", tool.Name)
		}
	}
}

func TestGraphSearchReturnsCitedPathsAndChunks(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	g.seedChain(t)
	res := g.call(t, "kb_graph_search", map[string]any{"query": "Who is on call for the team that owns the service whose backend suffered the Vane-3 outage?", "collection": "kg", "hops": 3})
	if res.IsError {
		t.Fatal(text(t, res))
	}
	txt := text(t, res)
	first, _, _ := strings.Cut(txt, "\n")
	if first != UntrustedNotice {
		t.Fatalf("first line = %q", first)
	}
	for _, want := range []string{
		"Vane-3 <-runs_in- Teal <-stores_data_in- Quill <-calls- Orion", // a 3-hop path from the seed, against edge direction
		"document_id=d-region", "document_id=d-store", "document_id=d-calls",
		"https://src/d-region", "Cluster Teal runs in region Vane-3 and had a disk outage", // supporting chunk text
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "d-oncall") {
		t.Errorf("a fact 5 hops away leaked:\n%s", txt)
	}
	out := structured[GraphSearchOutput](t, res)
	if len(out.Paths) == 0 || len(out.Chunks) == 0 {
		t.Fatalf("structured: %+v", out)
	}
	longest := out.Paths[0]
	if len(longest.Edges) != 3 || longest.Edges[0].DocumentID != "d-region" || longest.Edges[0].SourceURI != "https://src/d-region" || longest.Edges[0].Evidence == "" {
		t.Fatalf("first path = %+v", longest)
	}
	// Every document id of a result resolves with kb_get_document.
	for _, p := range out.Paths {
		for _, e := range p.Edges {
			r := g.call(t, "kb_get_document", map[string]any{"document_id": e.DocumentID, "collection": p.Collection})
			if r.IsError {
				t.Errorf("kb_get_document(%s): %s", e.DocumentID, text(t, r))
			}
		}
	}
	// A hop-limited path ends with a hint on how to continue.
	if !strings.Contains(txt, "kb_graph_entity") {
		t.Errorf("no hint for continuing past the hop limit:\n%s", txt)
	}
}

func TestGraphSearchHopsDefaultAndClamp(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	g.seedChain(t)
	maxEdges := func(args map[string]any) int {
		args["query"], args["collection"] = "Vane-3", "kg"
		res := g.call(t, "kb_graph_search", args)
		if res.IsError {
			t.Fatal(text(t, res))
		}
		n := 0
		for _, p := range structured[GraphSearchOutput](t, res).Paths {
			n = max(n, len(p.Edges))
		}
		return n
	}
	if got := maxEdges(map[string]any{}); got != 2 {
		t.Errorf("default hops: longest path has %d edges, want 2", got)
	}
	if got := maxEdges(map[string]any{"hops": 1}); got != 1 {
		t.Errorf("hops=1: %d", got)
	}
	if got := maxEdges(map[string]any{"hops": 99}); got != 3 {
		t.Errorf("hops clamp: %d", got)
	}
}

func TestGraphSearchSeedsByEmbeddingAndDeterminism(t *testing.T) {
	g := setupGraph(t, true, axisEmbedder{})
	g.seedChain(t)
	ctx := context.Background()
	// Give Quill a name embedding on the "ledger" axis; the query names nothing.
	if _, err := g.st.UpsertEntity(ctx, store.EntityInput{Collection: "kg", Name: "Quill", Type: "service", NameEmbedding: []float32{1, 0, 0}}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"query": "which ledger is involved?", "collection": "kg", "hops": 1}
	a := g.call(t, "kb_graph_search", args)
	if a.IsError || !strings.Contains(text(t, a), "Quill") || !strings.Contains(text(t, a), "-calls->") {
		t.Fatalf("embedding seed not used: %v %s", a.IsError, text(t, a))
	}
	if b := g.call(t, "kb_graph_search", args); text(t, a) != text(t, b) {
		t.Fatalf("not deterministic:\n%s\n---\n%s", text(t, a), text(t, b))
	}
}

func TestGraphSearchIsBoundedOnHubs(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	ctx := context.Background()
	long := strings.Repeat("word ", 90) // chunk text well above the per-chunk bound
	for i := 0; i < 80; i++ {
		g.put(t, "hub", fmt.Sprintf("doc%02d", i), "Doc", long)
	}
	hub, _ := g.st.UpsertEntity(ctx, store.EntityInput{Collection: "hub", Name: "Hubby", Type: "service"})
	for i := 0; i < 80; i++ {
		n, _ := g.st.UpsertEntity(ctx, store.EntityInput{Collection: "hub", Name: fmt.Sprintf("Leaf %02d", i), Type: "service"})
		if _, err := g.st.AddRelation(ctx, store.Relation{Collection: "hub", SrcID: hub.ID, DstID: n.ID, Predicate: "calls", Evidence: strings.Repeat("e", 600), DocumentID: fmt.Sprintf("doc%02d", i), PromptVersion: "v1", Model: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	res := g.call(t, "kb_graph_search", map[string]any{"query": "Hubby", "collection": "hub", "hops": 3})
	if res.IsError {
		t.Fatal(text(t, res))
	}
	txt := text(t, res)
	if len(txt) > 12000 {
		t.Errorf("result is %d bytes, want at most 12000", len(txt))
	}
	out := structured[GraphSearchOutput](t, res)
	if len(out.Paths) > 10 || len(out.Chunks) > 6 {
		t.Errorf("paths=%d chunks=%d exceed the bounds", len(out.Paths), len(out.Chunks))
	}
	if !out.Truncated || !strings.Contains(txt, "more paths") {
		t.Errorf("truncation not reported:\n%.400s", txt)
	}
}

func TestGraphSearchCollectionScoping(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	g.seedChain(t)
	g.put(t, "other", "o1", "O", "Orion also exists elsewhere.")
	oa, _ := g.st.UpsertEntity(context.Background(), store.EntityInput{Collection: "other", Name: "Orion", Type: "service"})
	ob, _ := g.st.UpsertEntity(context.Background(), store.EntityInput{Collection: "other", Name: "Zeta", Type: "service"})
	if _, err := g.st.AddRelation(context.Background(), store.Relation{Collection: "other", SrcID: oa.ID, DstID: ob.ID, Predicate: "calls", Evidence: "x", DocumentID: "o1"}); err != nil {
		t.Fatal(err)
	}
	scoped := text(t, g.call(t, "kb_graph_search", map[string]any{"query": "Orion", "collection": "kg"}))
	if strings.Contains(scoped, "Zeta") || !strings.Contains(scoped, "Quill") {
		t.Errorf("collection filter:\n%s", scoped)
	}
	all := text(t, g.call(t, "kb_graph_search", map[string]any{"query": "Orion"}))
	if !strings.Contains(all, "Zeta") || !strings.Contains(all, "Quill") || !strings.Contains(all, "collection=other") {
		t.Errorf("all collections:\n%s", all)
	}
}

func TestGraphSearchNothingFoundIsNotAnError(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	g.seedChain(t)
	res := g.call(t, "kb_graph_search", map[string]any{"query": "completely unrelated zebra", "collection": "kg"})
	if res.IsError || !strings.Contains(text(t, res), "No entities") {
		t.Fatalf("%v %s", res.IsError, text(t, res))
	}
	if out := structured[GraphSearchOutput](t, res); out.Paths == nil || out.Chunks == nil {
		t.Errorf("empty lists must be []: %+v", out)
	}
}

func TestGraphEntity(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	g.seedChain(t)
	ctx := context.Background()
	if _, err := g.st.UpsertEntity(ctx, store.EntityInput{Collection: "kg", Name: "Orion", Type: "service", Description: "The checkout service", Aliases: []string{"Orion-svc"}}); err != nil {
		t.Fatal(err)
	}
	res := g.call(t, "kb_graph_entity", map[string]any{"name": "orion", "collection": "kg"})
	if res.IsError {
		t.Fatal(text(t, res))
	}
	txt := text(t, res)
	first, _, _ := strings.Cut(txt, "\n")
	if first != UntrustedNotice {
		t.Fatalf("first line = %q", first)
	}
	for _, want := range []string{"Orion", "service", "The checkout service", "orion svc", "Basalt -owns-> Orion", "Orion -calls-> Quill", "document_id=d-owner", "document_id=d-calls"} {
		if !strings.Contains(txt, want) {
			t.Errorf("lacks %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "Teal") {
		t.Errorf("a two-hop neighbour is listed:\n%s", txt)
	}
	out := structured[GraphEntityOutput](t, res)
	if !out.Found || out.Name != "Orion" || len(out.Neighbours) != 2 {
		t.Fatalf("structured: %+v", out)
	}

	res = g.call(t, "kb_graph_entity", map[string]any{"name": "Nobody Atall", "collection": "kg"})
	if res.IsError || !strings.Contains(text(t, res), "no entity named") {
		t.Fatalf("missing entity: %v %s", res.IsError, text(t, res))
	}
}

func TestGraphEntityAmbiguousNamesAreListed(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	g.seedChain(t)
	if _, err := g.st.UpsertEntity(context.Background(), store.EntityInput{Collection: "kg", Name: "Mira Lindqvist", Type: "person"}); err != nil {
		t.Fatal(err)
	}
	res := g.call(t, "kb_graph_entity", map[string]any{"name": "Mira", "collection": "kg"})
	txt := text(t, res)
	if res.IsError || !strings.Contains(txt, "Mira Okafor") || !strings.Contains(txt, "Mira Lindqvist") || !strings.Contains(strings.ToLower(txt), "more than one") {
		t.Fatalf("ambiguity: %v %s", res.IsError, txt)
	}
	if structured[GraphEntityOutput](t, res).Found {
		t.Error("an ambiguous name must not be reported as found")
	}
}

func TestGraphPaths(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	g.seedChain(t)
	res := g.call(t, "kb_graph_paths", map[string]any{"from": "Orion", "to": "Mira Okafor", "collection": "kg", "max_hops": 3})
	if res.IsError {
		t.Fatal(text(t, res))
	}
	txt := text(t, res)
	first, _, _ := strings.Cut(txt, "\n")
	if first != UntrustedNotice {
		t.Fatalf("first line = %q", first)
	}
	for _, want := range []string{"Orion <-owns- Basalt -has_on_call-> Mira Okafor", "document_id=d-owner", "document_id=d-oncall", "Team Basalt's on-call engineer is Mira Okafor."} {
		if !strings.Contains(txt, want) {
			t.Errorf("lacks %q:\n%s", want, txt)
		}
	}
	out := structured[GraphPathsOutput](t, res)
	if len(out.Paths) != 1 || len(out.Paths[0].Edges) != 2 {
		t.Fatalf("structured: %+v", out)
	}

	res = g.call(t, "kb_graph_paths", map[string]any{"from": "Orion", "to": "Vane-3", "collection": "kg", "max_hops": 2})
	if res.IsError || !strings.Contains(text(t, res), "no path within 2 hops") {
		t.Fatalf("no path: %v %s", res.IsError, text(t, res))
	}
	res = g.call(t, "kb_graph_paths", map[string]any{"from": "Orion", "to": "Ghostly Thing", "collection": "kg"})
	if res.IsError || !strings.Contains(text(t, res), `no entity named "Ghostly Thing"`) {
		t.Fatalf("missing entity: %v %s", res.IsError, text(t, res))
	}
	res = g.call(t, "kb_graph_paths", map[string]any{"from": "Orion", "to": "Orion", "collection": "kg"})
	if res.IsError || !strings.Contains(text(t, res), "same entity") {
		t.Fatalf("same entity: %v %s", res.IsError, text(t, res))
	}
}

func TestGraphToolArgumentValidation(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	for tool, args := range map[string]map[string]any{
		"kb_graph_search": {"query": "   "},
		"kb_graph_entity": {"name": ""},
		"kb_graph_paths":  {"from": "A", "to": "  "},
	} {
		res := g.call(t, tool, args)
		if !res.IsError {
			t.Errorf("%s: blank argument accepted", tool)
		}
	}
	for tool, args := range map[string]map[string]any{
		"kb_graph_search": {"query": "x", "collection": "no/good"},
		"kb_graph_entity": {"name": "x", "collection": "no/good"},
		"kb_graph_paths":  {"from": "a", "to": "b", "collection": "no/good"},
	} {
		res := g.call(t, tool, args)
		if !res.IsError || !strings.Contains(text(t, res), "collection") {
			t.Errorf("%s: bad collection: %v %s", tool, res.IsError, text(t, res))
		}
	}
	res := g.call(t, "kb_graph_search", map[string]any{"query": strings.Repeat("x", 3000)})
	if !res.IsError {
		t.Error("overlong query accepted")
	}
}

type slowGraphStore struct{ *memory.Store }

func (slowGraphStore) FindEntitiesByName(ctx context.Context, _, _ string, _ int) ([]store.EntityHit, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestGraphToolsHonourTheCallDeadline(t *testing.T) {
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	st := slowGraphStore{memory.New(0)}
	svc := &kb.Service{Store: st, Embedder: embed.None{}, Log: log, Graph: &kb.GraphOptions{MaxHops: 3}}
	if _, err := svc.Put(context.Background(), "c", "d", kb.DocInput{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", New(svc, log, Options{Stateless: true, Timeout: 100 * time.Millisecond}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	start := time.Now()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "kb_graph_entity", Arguments: map[string]any{"name": "anything", "collection": "c"}})
	if err != nil || !res.IsError || !strings.Contains(text(t, res), "timed out") || time.Since(start) > 5*time.Second {
		t.Fatalf("deadline: err=%v res=%+v after %v", err, res, time.Since(start))
	}
}

func TestGraphLogsNeverContainContent(t *testing.T) {
	g := setupGraph(t, true, embed.None{})
	g.seedChain(t)
	g.call(t, "kb_graph_search", map[string]any{"query": "secret-query-term Orion", "collection": "kg"})
	g.call(t, "kb_graph_entity", map[string]any{"name": "Orion", "collection": "kg"})
	g.call(t, "kb_graph_paths", map[string]any{"from": "Orion", "to": "Mira Okafor", "collection": "kg"})
	logs := g.logs.String()
	for _, banned := range []string{"secret-query-term", "Mira Okafor", "Quill", "on-call engineer", "d-owner"} {
		if strings.Contains(logs, banned) {
			t.Fatalf("logs contain %q:\n%s", banned, logs)
		}
	}
	for _, tool := range []string{"kb_graph_search", "kb_graph_entity", "kb_graph_paths"} {
		if !strings.Contains(logs, tool) {
			t.Errorf("%s call not logged", tool)
		}
	}
}
