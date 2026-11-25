package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExamples(t *testing.T) {
	c, err := LoadCatalog("../../testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.Adapters.Runtime != "kagent" || c.Namespaces.Tools != "mcp-tools" || len(c.Profiles) != 1 {
		t.Fatalf("%+v", c)
	}
	p, ok := c.Profile("lookup")
	if !ok || p.ToolServer != "toolhive-vmcp-lookup" || !p.CodeMode {
		t.Fatalf("profile: %+v %v", p, ok)
	}
	if !c.HasModel("claude-haiku") || c.HasModel("gpt") {
		t.Fatal("HasModel")
	}
	pol, err := LoadPolicy("../../testdata/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if pol.MaxAgents != 5 || !pol.Memory.Allowed || pol.Memory.MaxTTLDays != 30 {
		t.Fatalf("%+v", pol)
	}
}

func write(t *testing.T, s string) string {
	p := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadCatalogRefusals(t *testing.T) {
	base, _ := os.ReadFile("../../testdata/catalog.json")
	cases := map[string]struct{ from, to, want string }{
		"unknown field":     {`"models"`, `"modles": [], "models"`, "unknown field"},
		"skill not digest":  {"@sha256:0000000000000000000000000000000000000000000000000000000000000000", "@latest", "digest-pinned"},
		"no runtime":        {`"runtime": "kagent"`, `"runtime": ""`, "adapters.runtime"},
		"no harness image":  {`"workerPool": "kagent-default"`, `"workerPool": ""`, "workerPool"},
		"bad gateway url":   {`"gatewayBaseUrl": "http`, `"gatewayBaseUrl": "ftp`, "gatewayBaseUrl"},
		"profile no server": {`"toolServer": "toolhive-vmcp-lookup"`, `"toolServer": ""`, "toolServer"},
		"memory no prompt":  {`"modelConfig": "gateway-embed-memory"`, `"modelConfig": ""`, "memory.modelConfig"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := strings.Replace(string(base), c.from, c.to, 1)
			if s == string(base) {
				t.Fatal("test input not changed")
			}
			_, err := LoadCatalog(write(t, s))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
	if _, err := LoadCatalog(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file must fail")
	}
}

func TestLoadPolicyRefusals(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"negative max":                 {`{"maxAgents": -1}`, "maxAgents"},
		"unknown field":                {`{"maxAgents": 1, "allowAll": true}`, "unknown field"},
		"bad origin":                   {`{"maxAgents": 1, "egress": {"allowedOrigins": ["api.osv.dev"]}}`, "allowedOrigins"},
		"ttl negative":                 {`{"maxAgents": 1, "memory": {"allowed": true, "maxTtlDays": -3}}`, "maxTtlDays"},
		"memory allowed without a cap": {`{"maxAgents": 1, "memory": {"allowed": true}}`, "maxTtlDays"},
		"trailing json":                {`{"maxAgents": 1} {}`, "trailing"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadPolicy(write(t, c.in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

func TestPolicyAllows(t *testing.T) {
	p := Policy{AllowedModels: []string{"a"}, AllowedProfiles: []string{"*"}}
	if !p.ModelAllowed("a") || p.ModelAllowed("b") {
		t.Fatal("exact match")
	}
	if !p.ProfileAllowed("anything") {
		t.Fatal("star allows every catalog profile")
	}
	if (&Policy{}).SkillAllowed("x") {
		t.Fatal("empty list allows nothing")
	}
}
