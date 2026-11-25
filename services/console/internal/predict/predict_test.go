package predict

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/console/internal/model"
	"github.com/openagentix/openagentix/services/console/internal/platform"
)

func load(t *testing.T) platform.Both {
	t.Helper()
	var b platform.Both
	for p, v := range map[string]any{"../fake/testdata/catalog.json": &b.Catalog, "../fake/testdata/policy.json": &b.Policy} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func good() model.Spec {
	return model.Spec{Model: "claude-haiku", Instructions: "x", ToolProfile: "lookup", Tools: []string{"osv_query_vulnerability"}}
}

func has(ps []model.Prediction, field, sub string) bool {
	for _, p := range ps {
		if p.Field == field && strings.Contains(p.Message, sub) {
			return true
		}
	}
	return false
}

func TestNothingForAGoodDraft(t *testing.T) {
	if ps := Check(Input{Name: "ok", Spec: good(), P: load(t)}); len(ps) != 0 {
		t.Fatalf("got %v", ps)
	}
}

func TestRules(t *testing.T) {
	b := load(t)
	type tc struct {
		name   string
		mut    func(*model.Spec)
		field  string
		substr string
		agent  string
		others int
	}
	for _, c := range []tc{
		{name: "unknown model", mut: func(s *model.Spec) { s.Model = "gpt-x" }, field: "model", substr: "not in the platform catalog"},
		{name: "unknown profile", mut: func(s *model.Spec) { s.ToolProfile = "nope" }, field: "toolProfile", substr: "is not defined"},
		{name: "tool not in profile", mut: func(s *model.Spec) { s.Tools = append(s.Tools, "knowledge_kb_get_document") }, field: "tools",
			substr: `tool "knowledge_kb_get_document" which is not in profile "lookup"`},
		{name: "no profile", mut: func(s *model.Spec) { s.ToolProfile = ""; s.Tools = nil }, field: "toolProfile", substr: "requires a toolProfile"},
		{name: "knowledge needs tools", mut: func(s *model.Spec) { s.Knowledge = &model.Feature{Enabled: true} }, field: "knowledge", substr: "knowledge_kb_get_document"},
		{name: "memory ttl", mut: func(s *model.Spec) { s.Memory = &model.Memory{Enabled: true, TTLDays: 90} }, field: "memory.ttlDays", substr: "above the platform maximum of 30"},
		{name: "skill", mut: func(s *model.Spec) { s.Skills = []model.Skill{{Name: "mystery"}} }, field: "skills", substr: "not in the platform catalog"},
		{name: "egress", mut: func(s *model.Spec) { s.Egress = &model.Egress{Allow: []string{"https://services.nvd.nist.gov"}} }, field: "egress.allow",
			substr: `egress.allow entry "https://services.nvd.nist.gov" is not matched by the platform policy`},
		{name: "egress shape", mut: func(s *model.Spec) { s.Egress = &model.Egress{Allow: []string{"https://x.org/path"}} }, field: "egress.allow", substr: "is not an origin"},
		{name: "reserved", mut: func(s *model.Spec) {}, agent: "assistant", field: "name", substr: "is reserved"},
		{name: "count", mut: func(s *model.Spec) {}, others: 5, field: "name", substr: "at most 5 agents and 5 already exist"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := good()
			c.mut(&s)
			n := c.agent
			if n == "" {
				n = "fresh"
			}
			if ps := Check(Input{Name: n, Spec: s, Others: c.others, P: b}); !has(ps, c.field, c.substr) {
				t.Fatalf("want %s/%q in %v", c.field, c.substr, ps)
			}
		})
	}
}

func TestEgressWildcard(t *testing.T) {
	pats := []string{"https://*.example.org"}
	for o, want := range map[string]bool{"https://a.example.org": true, "https://a.b.example.org": false, "https://example.org": false, "http://a.example.org": false, "https://*.example.org": true} {
		if got := OriginAllowed(o, pats); got != want {
			t.Errorf("%s: got %v want %v", o, got, want)
		}
	}
}
