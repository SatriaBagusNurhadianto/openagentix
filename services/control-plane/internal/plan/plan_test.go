package plan

import (
	"slices"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
)

type tools struct{ c *catalog.Catalog }

func must(p Plan, err error) Plan {
	if err != nil {
		panic(err)
	}
	return p
}

func (f tools) Profile(n string) (catalog.Profile, bool) { return f.c.Profile(n) }
func (f tools) Profiles() []string                       { return f.c.ProfileNames() }
func (f tools) SharedServer() string                     { return f.c.ToolServer }

func cat(t *testing.T) *catalog.Catalog {
	c, err := catalog.LoadCatalog("../../testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func def() *api.AgentDefinition {
	return &api.AgentDefinition{Metadata: api.ObjectMeta{Name: "x", Namespace: "kagent", UID: "0a1b2c3d-1111-4222-8333-444444444444"}, Spec: api.Spec{
		Description: "d", Model: "claude-haiku", Instructions: "Be brief.\n", ToolProfile: "lookup",
		Tools: []string{"osv_query_vulnerability"},
	}}
}

func TestBuildBasics(t *testing.T) {
	c := cat(t)
	p := must(Build(def(), c, tools{c}))
	if p.Name != "x" || p.KeySecret != "x-virtual-key-0a1b2c3d" || p.KeySecretKey != "OPENAI_API_KEY" || p.GatewayUserID != "x" {
		t.Fatalf("%+v", p)
	}
	if p.ToolServer != "toolhive-vmcp-lookup" || !slices.Equal(p.Tools, []string{"osv_query_vulnerability"}) {
		t.Fatalf("%+v", p)
	}
	if p.Prompt != "Be brief.\n" {
		t.Fatalf("instructions without additions pass through unchanged, got %q", p.Prompt)
	}
	if p.ModelConfigName != "gateway-x" || p.GatewayBaseURL != c.GatewayBaseURL {
		t.Fatalf("%+v", p)
	}
}

func TestPromptAdditionsAreTrimmedAndOrdered(t *testing.T) {
	c := cat(t)
	d := def()
	d.Spec.Knowledge, d.Spec.Notify = true, true
	d.Spec.Memory = api.Memory{Enabled: true, TTLDays: 7}
	p := must(Build(d, c, tools{c}))
	want := "Be brief.\n" + c.Knowledge.Prompt + "\n" + c.Notify.Prompt + "\n" + strings.TrimSpace(c.Memory.Prompt)
	want = strings.Replace(want, "Be brief.\n"+c.Knowledge.Prompt, "Be brief."+"\n"+strings.TrimSpace(c.Knowledge.Prompt), 1)
	if p.Prompt != want {
		t.Fatalf("got\n%q\nwant\n%q", p.Prompt, want)
	}
	if p.Memory == nil || p.Memory.TTLDays != 7 || p.Memory.ModelConfig != "gateway-embed-memory" {
		t.Fatalf("%+v", p.Memory)
	}
}

func TestExtraToolsFollowTheListedOnesWithoutDuplicates(t *testing.T) {
	c := cat(t)
	d := def()
	d.Spec.Tools = []string{"knowledge_kb_search", "osv_query_vulnerability"}
	d.Spec.Knowledge = true
	p := must(Build(d, c, tools{c}))
	want := []string{"knowledge_kb_search", "osv_query_vulnerability", "knowledge_kb_get_document", "knowledge_kb_list_collections", "knowledge_kb_graph_search", "knowledge_kb_graph_entity", "knowledge_kb_graph_paths"}
	if !slices.Equal(p.Tools, want) {
		t.Fatalf("got %v", p.Tools)
	}
}

func TestNoToolsNoServer(t *testing.T) {
	c := cat(t)
	d := def()
	d.Spec.ToolProfile, d.Spec.Tools = "", nil
	p := must(Build(d, c, tools{c}))
	if p.ToolServer != "" || len(p.Tools) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestSkillsResolveToCatalogRefsInListedOrder(t *testing.T) {
	c := cat(t)
	d := def()
	d.Spec.Skills = []string{"openagentix-canary"}
	p := must(Build(d, c, tools{c}))
	if len(p.Skills) != 1 || p.Skills[0].Name != "openagentix-canary" || p.Skills[0].Ref != c.Skills["openagentix-canary"] {
		t.Fatalf("%+v", p.Skills)
	}
}

func TestPlanDoesNotAliasTheDefinition(t *testing.T) {
	c := cat(t)
	d := def()
	d.Spec.Egress.Allow = []string{"https://api.osv.dev"}
	p := must(Build(d, c, tools{c}))
	p.Egress[0] = "changed"
	p.Tools[0] = "changed"
	if d.Spec.Egress.Allow[0] != "https://api.osv.dev" || d.Spec.Tools[0] != "osv_query_vulnerability" {
		t.Fatal("plan shares memory with the definition")
	}
}

func TestKeySecretNameCarriesTheUID(t *testing.T) {
	c := cat(t)
	d := def()
	d.Metadata.UID = "0a1b2c3d-1111-4222-8333-444444444444"
	p, err := Build(d, c, tools{c})
	if err != nil {
		t.Fatal(err)
	}
	if p.KeySecret != "x-virtual-key-0a1b2c3d" {
		t.Fatalf("got %q", p.KeySecret)
	}
}

func TestBuildFailsWithoutAUsableUID(t *testing.T) {
	c := cat(t)
	for _, uid := range []string{"", "short", "zzzzzzzz-1111-4222-8333-444444444444"} {
		d := def()
		d.Metadata.UID = uid
		if _, err := Build(d, c, tools{c}); err == nil {
			t.Fatalf("uid %q: want an error, the Secret name needs 8 hex characters of it", uid)
		}
	}
}

func TestBuildFailsInsteadOfFallingBackToTheSharedToolServer(t *testing.T) {
	c := cat(t)
	d := def()
	d.Metadata.UID = "0a1b2c3d-1111-4222-8333-444444444444"
	d.Spec.ToolProfile = "ghost"
	if p, err := Build(d, c, tools{c}); err == nil {
		t.Fatalf("an unknown profile must be an error, got server %q", p.ToolServer)
	}
	d.Spec.ToolProfile = ""
	if p, err := Build(d, c, tools{c}); err == nil {
		t.Fatalf("tools without a profile must be an error, got server %q", p.ToolServer)
	}
}
