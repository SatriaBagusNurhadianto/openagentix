package policy

import (
	"slices"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
)

// ---- M6: every free-text field is scanned, and the platform's own keys are a secret shape ----

func TestSecretShapesAreFoundInEveryFreeTextField(t *testing.T) {
	secret := "s" + "k-" + strings.Repeat("Z", 40)
	for field, set := range map[string]func(d *api.AgentDefinition){
		"description": func(d *api.AgentDefinition) { d.Spec.Description = "uses " + secret },
		"toolProfile": func(d *api.AgentDefinition) { d.Spec.ToolProfile = secret },
		"tools":       func(d *api.AgentDefinition) { d.Spec.Tools = append(d.Spec.Tools, secret) },
		"skills":      func(d *api.AgentDefinition) { d.Spec.Skills = []api.Skill{{Name: secret}} },
		"model":       func(d *api.AgentDefinition) { d.Spec.Model = secret },
	} {
		t.Run(field, func(t *testing.T) {
			v := run(t, func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) { set(d) })
			want := field + " contains something shaped like a secret (provider_secret_key)"
			if !slices.ContainsFunc(v, func(x string) bool { return strings.Contains(x, want) }) {
				t.Fatalf("want %q, got %q", want, v)
			}
			for _, x := range v {
				if strings.Contains(x, secret) {
					t.Fatalf("a violation repeats the matched text: %q", x)
				}
			}
		})
	}
}

func TestPlatformVirtualKeyIsASecretShape(t *testing.T) {
	key := "vk-" + strings.Repeat("0123456789abcdef", 2)
	v := run(t, func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
		d.Spec.Instructions = "key is " + key
	})
	if !slices.ContainsFunc(v, func(x string) bool { return strings.Contains(x, "(platform_virtual_key)") }) {
		t.Fatalf("got %q", v)
	}
	if v := run(t, func(d *api.AgentDefinition, _ *catalog.Catalog, _ *catalog.Policy) {
		d.Spec.Instructions = "the vk-short prefix alone is fine"
	}); len(v) != 0 {
		t.Fatalf("false positive: %q", v)
	}
}

// ---- M7: egress to cluster-internal names and addresses is refused whatever the policy says ----

func TestEgressToInternalHostsIsRefusedRegardlessOfPolicy(t *testing.T) {
	for _, origin := range []string{
		"https://localhost", "https://redis:6379", "https://metadata",
		"https://api.default.svc", "https://api.default.svc:8443", "https://api.default.svc.cluster.local", "https://x.cluster.local",
		"https://api.kagent", "https://x.kagent.svc", "https://x.kagent.svc.cluster.local",
		"https://gw.agentgateway-system", "http://model-gateway.agentgateway-system.svc.cluster.local",
		"https://10.0.0.1", "https://127.0.0.1", "https://169.254.169.254", "https://192.168.1.5:8080", "https://172.16.0.9", "https://[::1]", "https://0.0.0.0",
	} {
		t.Run(origin, func(t *testing.T) {
			v := run(t, func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
				d.Spec.Egress.Allow = []string{origin}
				p.Egress.AllowedOrigins = []string{origin, "https://*.svc", "https://*.cluster.local", "https://*.kagent.svc"} // the policy allows it; it must not matter
			})
			if !slices.ContainsFunc(v, func(x string) bool { return strings.Contains(x, "egress.allow entry "+`"`+origin+`"`) }) {
				t.Fatalf("want a violation for %s, got %q", origin, v)
			}
		})
	}
}

func TestPublicHostsStillPassTheEgressRules(t *testing.T) {
	for _, origin := range []string{"https://api.osv.dev", "https://docs.example.org:8443"} {
		v := run(t, func(d *api.AgentDefinition, _ *catalog.Catalog, p *catalog.Policy) {
			d.Spec.Egress.Allow = []string{origin}
			p.Egress.AllowedOrigins = []string{origin}
		})
		if len(v) != 0 {
			t.Fatalf("%s: %q", origin, v)
		}
	}
}
