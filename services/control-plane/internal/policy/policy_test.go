package policy

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
)

// fakeTools is a ToolResolver that does not depend on the toolhive adapter.
type fakeTools struct{ c *catalog.Catalog }

func (f fakeTools) Profile(n string) (catalog.Profile, bool) { return f.c.Profile(n) }
func (f fakeTools) Profiles() []string                       { return f.c.ProfileNames() }
func (f fakeTools) SharedServer() string                     { return f.c.ToolServer }

func load(t *testing.T) (*catalog.Catalog, *catalog.Policy) {
	t.Helper()
	c, err := catalog.LoadCatalog("../../testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := catalog.LoadPolicy("../../testdata/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

// good is the demo agent profile-demo as a definition.
func good() *api.AgentDefinition {
	return &api.AgentDefinition{
		APIVersion: api.APIVersion, Kind: api.Kind,
		Metadata: api.ObjectMeta{Name: "researcher", Namespace: "kagent"},
		Spec: api.Spec{
			Description:  "Looks things up",
			Model:        "claude-haiku",
			Instructions: "You answer questions about software vulnerabilities.",
			ToolProfile:  "lookup",
			Tools:        []string{"execute_tool_script", "osv_query_vulnerability", "osv_get_vulnerability", "knowledge_kb_search"},
			Egress:       api.Egress{Allow: []string{"https://api.osv.dev"}},
		},
	}
}

func run(t *testing.T, mutate func(*api.AgentDefinition, *catalog.Catalog, *catalog.Policy), earlier ...string) []string {
	t.Helper()
	c, p := load(t)
	d := good()
	if mutate != nil {
		mutate(d, c, p)
	}
	return Validate(Input{Def: d, Catalog: c, Policy: p, Tools: fakeTools{c}, Earlier: earlier})
}

func TestAccepted(t *testing.T) {
	cases := map[string]func(*api.AgentDefinition, *catalog.Catalog, *catalog.Policy){
		"the demo agent as a definition": nil,
		"no tools and no profile when the policy does not require one": func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.ToolProfile, d.Spec.Tools, d.Spec.Egress.Allow = "", nil, nil
			p.RequireToolProfile = false
		},
		"memory within the limit": func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Memory = api.Memory{Enabled: true, TTLDays: 30}
		},
		"memory without ttl uses the runtime default": func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Memory = api.Memory{Enabled: true}
		},
		"egress matched by a wildcard pattern": func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Egress.Allow = []string{"https://docs.example.org"}
		},
		"wildcard policy lists": func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			p.AllowedModels, p.AllowedProfiles, p.AllowedSkills = []string{"*"}, []string{"*"}, []string{"*"}
			d.Spec.Skills = []string{"openagentix-canary"}
		},
		"knowledge and notify with a profile that offers their tools": func(d *api.AgentDefinition, c *catalog.Catalog, _ *catalog.Policy) {
			c.Profiles[0].Tools = append(c.Profiles[0].Tools, c.Knowledge.Tools...)
			c.Profiles[0].Tools = append(c.Profiles[0].Tools, c.Notify.Tools...)
			d.Spec.Knowledge, d.Spec.Notify = true, true
		},
		"a name at the length limit": func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Metadata.Name = strings.Repeat("a", 40)
		},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if v := run(t, m); len(v) != 0 {
				t.Fatalf("want accepted, got %q", v)
			}
		})
	}
}

type mut = func(*api.AgentDefinition, *catalog.Catalog, *catalog.Policy)

func TestRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate mut
		want   string // substring of one violation
	}{
		// model, profile, skill: catalog and policy
		{"model not in catalog", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { d.Spec.Model = "gpt-9" }, `model "gpt-9" is not in the platform catalog (claude-haiku)`},
		{"model not allowed", func(d *api.AgentDefinition, c *catalog.Catalog, _ *catalog.Policy) {
			c.Models = append(c.Models, "big")
			d.Spec.Model = "big"
		}, `model "big" is not allowed by the platform policy`},
		{"profile not in catalog", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { d.Spec.ToolProfile = "ghost" }, `toolProfile "ghost" is not defined (defined: lookup)`},
		{"profile not allowed", func(_ *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) { p.AllowedProfiles = nil }, `toolProfile "lookup" is not allowed by the platform policy`},
		{"skill not in catalog", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { d.Spec.Skills = []string{"ghost"} }, `skill "ghost" is not in the platform catalog`},
		{"skill not allowed", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.Skills = []string{"openagentix-canary"}
			p.AllowedSkills = nil
		}, `skill "openagentix-canary" is not allowed by the platform policy`},
		// tools
		{"tools without a profile", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.ToolProfile = ""
			p.RequireToolProfile = false
		}, "lists tools but names no toolProfile"},
		{"policy requires a profile", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.ToolProfile, d.Spec.Tools = "", nil
		}, "the platform policy requires a toolProfile"},
		{"tool not offered by the profile", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Tools = append(d.Spec.Tools, "fetch_fetch")
		}, `lists tool "fetch_fetch" which is not in profile "lookup" (the profile offers: knowledge_kb_search, osv_query_vulnerability, osv_get_vulnerability, execute_tool_script)`},
		{"code mode off", func(d *api.AgentDefinition, c *catalog.Catalog, _ *catalog.Policy) {
			c.Profiles[0].CodeMode = false
			c.Profiles[0].Tools = c.Profiles[0].Tools[:3]
		}, `lists tool "execute_tool_script" but profile "lookup" has codeMode off`},
		{"tool listed twice", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Tools = append(d.Spec.Tools, "osv_query_vulnerability")
		}, `lists tool "osv_query_vulnerability" twice`},
		// knowledge and notify
		{"knowledge not allowed by policy", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.Knowledge = true
			p.AllowKnowledge = false
		}, "knowledge is not allowed by the platform policy"},
		{"knowledge service off", func(d *api.AgentDefinition, c *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Knowledge = true
			c.Knowledge.Available = false
		}, "knowledge is not available on this platform"},
		{"knowledge tools not in the profile", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { d.Spec.Knowledge = true }, `lists tool "knowledge_kb_get_document" which is not in profile "lookup"`},
		{"knowledge needs a profile", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.Knowledge, d.Spec.ToolProfile, d.Spec.Tools = true, "", nil
			p.RequireToolProfile = false
		}, "knowledge adds tools, so it needs a toolProfile"},
		{"notify not allowed by policy", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.Notify = true
			p.AllowNotify = false
		}, "notify is not allowed by the platform policy"},
		{"notify service off", func(d *api.AgentDefinition, c *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Notify = true
			c.Notify.Available = false
		}, "notify is not available on this platform"},
		{"notify needs a profile", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.Notify, d.Spec.ToolProfile, d.Spec.Tools = true, "", nil
			p.RequireToolProfile = false
		}, "notify adds tools, so it needs a toolProfile"},
		// memory
		{"memory not allowed by policy", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.Memory = api.Memory{Enabled: true}
			p.Memory.Allowed = false
		}, "memory is not allowed by the platform policy"},
		{"memory stack not installed", func(d *api.AgentDefinition, c *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Memory = api.Memory{Enabled: true}
			c.Memory.Available = false
		}, "memory is not installed on this platform"},
		{"memory expiry job off", func(d *api.AgentDefinition, c *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Memory = api.Memory{Enabled: true}
			c.Memory.ExpiryEnabled = false
		}, "the memory expiry job is off, so the time limit would not be enforced"},
		{"ttl above the maximum", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Memory = api.Memory{Enabled: true, TTLDays: 31}
		}, "memory.ttlDays 31 is above the platform maximum of 30"},
		{"default ttl above the maximum", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.Memory = api.Memory{Enabled: true}
			p.Memory.MaxTTLDays = 10
		}, "memory.ttlDays 15 is above the platform maximum of 10"},
		{"ttl negative", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Memory = api.Memory{Enabled: true, TTLDays: -1}
		}, "memory.ttlDays must be at least 1"},
		// egress
		{"egress malformed", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Egress.Allow = []string{"api.osv.dev/v1"}
		}, `egress.allow entry "api.osv.dev/v1" is not an origin`},
		{"egress inside the tools namespace", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.Egress.Allow = []string{"http://vmcp-tools.mcp-tools.svc.cluster.local:4483"}
			p.Egress.AllowedOrigins = []string{"*"} // ignored: "*" is not a pattern
		}, `egress.allow entry "http://vmcp-tools.mcp-tools.svc.cluster.local:4483" is inside the tools namespace "mcp-tools"`},
		{"egress inside the tools namespace, short name", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Egress.Allow = []string{"http://osv.mcp-tools"}
		}, "is inside the tools namespace"},
		{"egress not matched by the policy", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Egress.Allow = []string{"https://api.github.com"}
		}, `egress.allow entry "https://api.github.com" is not matched by the platform policy`},
		{"egress wildcard broader than the policy", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Egress.Allow = []string{"https://*.osv.dev"}
		}, "is not matched by the platform policy"},
		{"egress two labels under a one-label pattern", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Egress.Allow = []string{"https://a.b.example.org"}
		}, "is not matched by the platform policy"},
		{"egress scheme differs", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Egress.Allow = []string{"http://api.osv.dev"}
		}, "is not matched by the platform policy"},
		{"egress default is none", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) { p.Egress.AllowedOrigins = nil }, "is not matched by the platform policy"},
		{"egress twice", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Egress.Allow = []string{"https://api.osv.dev", "https://api.osv.dev"}
		}, `lists "https://api.osv.dev" twice`},
		{"egress more than 64", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			var l []string
			for i := range 65 {
				l = append(l, fmt.Sprintf("https://h%d.example.org", i))
			}
			d.Spec.Egress.Allow = l
		}, "egress.allow has 65 origins; at most 64 are allowed"},
		// names, size, count
		{"name reserved by the catalog", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { d.Metadata.Name = "assistant" }, `name "assistant" is reserved`},
		{"name kb reserved", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { d.Metadata.Name = "kb-extract" }, `name "kb-extract" is reserved`},
		{"name reserved by the policy", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			p.ReservedNames = []string{"researcher"}
		}, `name "researcher" is reserved`},
		{"name too long", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Metadata.Name = strings.Repeat("a", 41)
		}, "at most 40 characters"},
		{"name not a DNS label", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { d.Metadata.Name = "Bad_Name" }, "must be a DNS label"},
		{"too many definitions", func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) { p.MaxAgents = 2 }, "the platform policy allows at most 2 agents"},
		{"description too long", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Description = strings.Repeat("d", 257)
		}, "description is 257 characters; at most 256"},
		{"instructions empty", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { d.Spec.Instructions = "  \n" }, "instructions are required"},
		{"instructions too long", func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
			d.Spec.Instructions = strings.Repeat("i", 16385)
		}, "instructions are 16385 bytes; at most 16384"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			earlier := []string(nil)
			if tc.name == "too many definitions" {
				earlier = []string{"a", "b"}
			}
			v := run(t, tc.mutate, earlier...)
			if !slices.ContainsFunc(v, func(s string) bool { return strings.Contains(s, tc.want) }) {
				t.Fatalf("want a violation containing %q, got %q", tc.want, v)
			}
		})
	}
}

// secrets builds each of the ten shapes at run time, so the source holds no literal that looks like a credential.
func secrets() map[string]string {
	return map[string]string{
		"aws_access_key_id":   "AK" + "IA" + "ABCDEFGHIJKLMNOP",
		"bearer_token":        "Bearer " + strings.Repeat("a1", 14),
		"github_token":        "gh" + "p_" + strings.Repeat("A", 36),
		"google_api_key":      "AI" + "za" + strings.Repeat("b", 35),
		"jwt":                 "ey" + "J" + strings.Repeat("a", 12) + ".ey" + "J" + strings.Repeat("b", 12) + "." + strings.Repeat("c", 12),
		"private_key_header":  "-----BEGIN RSA " + "PRIVATE KEY-----",
		"provider_secret_key": "s" + "k-" + strings.Repeat("Z", 40),
		"slack_token":         "xo" + "xb-" + strings.Repeat("1", 12),
		"stripe_secret_key":   "s" + "k_live_" + strings.Repeat("9", 20),
		"telegram_bot_token":  "123456789:" + strings.Repeat("A", 35),
	}
}

func TestInstructionsWithSecretShapes(t *testing.T) {
	for name, s := range secrets() {
		t.Run(name, func(t *testing.T) {
			v := run(t, func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
				d.Spec.Instructions = "Use this when asked: " + s + " and nothing else."
			})
			want := "instructions contain something shaped like a secret (" + name + ")"
			if !slices.ContainsFunc(v, func(x string) bool { return strings.Contains(x, want) }) {
				t.Fatalf("want %q, got %q", want, v)
			}
			for _, x := range v {
				if strings.Contains(x, s) {
					t.Fatalf("a violation repeats the matched text: %q", x)
				}
			}
		})
	}
	// ordinary text, long words and a bare "Bearer" must pass
	for _, ok := range []string{"Send the bearer of this message a friendly answer.", "Use sk-short and AKIA alone.", strings.Repeat("word ", 500)} {
		if v := run(t, func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { d.Spec.Instructions = ok }); len(v) != 0 {
			t.Fatalf("false positive on %q: %q", ok[:20], v)
		}
	}
}

func TestSecretPatternNamesAreTheTenOfTheMemoryScanAndThePlatformKey(t *testing.T) {
	want := []string{"aws_access_key_id", "bearer_token", "github_token", "google_api_key", "jwt", "private_key_header", "provider_secret_key", "slack_token", "stripe_secret_key", "telegram_bot_token", "platform_virtual_key"}
	got := PatternNames()
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestManyViolationsAreAllReportedInOrder(t *testing.T) {
	v := run(t, func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
		d.Spec.Model = "gpt-9"
		d.Spec.ToolProfile = "ghost"
		d.Spec.Skills = []string{"ghost"}
	})
	if len(v) < 3 {
		t.Fatalf("want at least three, got %q", v)
	}
	if !strings.HasPrefix(v[0], "model ") {
		t.Fatalf("model comes first: %q", v)
	}
}
