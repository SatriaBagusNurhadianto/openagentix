package kagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
	"github.com/openagentix/openagentix/services/control-plane/internal/plan"
)

type tools struct{ c *catalog.Catalog }

func (f tools) Profile(n string) (catalog.Profile, bool) { return f.c.Profile(n) }
func (f tools) Profiles() []string                       { return f.c.ProfileNames() }
func (f tools) SharedServer() string                     { return f.c.ToolServer }

func loadCat(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.LoadCatalog("../../../testdata/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// flatten turns an object into path -> JSON text of each leaf. Empty maps and lists are leaves, so that `kagent: {}` counts.
func flatten(prefix string, v any, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			out[prefix] = "{}"
			return
		}
		for k, c := range x {
			flatten(prefix+"."+k, c, out)
		}
	case []any:
		if len(x) == 0 {
			out[prefix] = "[]"
			return
		}
		for i, c := range x {
			flatten(fmt.Sprintf("%s[%d]", prefix, i), c, out)
		}
	default:
		b, _ := json.Marshal(x)
		out[prefix] = string(b)
	}
}

// ignoredGolden lists every path of the Helm output that the control plane does not produce, and why. Nothing else may
// differ. (The labels and ownerReferences the control plane adds are stamped by the core, not by this adapter, so the
// adapter output has none of them.)
var ignoredGolden = map[string]string{
	".metadata.labels.app.kubernetes.io/part-of":    "Helm-only label",
	".metadata.labels.app.kubernetes.io/managed-by": "Helm-only label (the control plane's own value is stamped by the core)",
	".metadata.labels.app.kubernetes.io/instance":   "Helm-only label",
	".metadata.labels.helm.sh/chart":                "Helm-only label",
}

func compare(t *testing.T, golden string, got []kube.Manifest) {
	t.Helper()
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	var want []map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("object count: golden %d, adapter %d", len(want), len(got))
	}
	for i := range want {
		w := map[string]string{}
		flatten("", want[i], w)
		for k := range ignoredGolden {
			delete(w, k)
		}
		g := map[string]string{}
		flatten("", got[i].Object, g)
		name := fmt.Sprintf("%v/%v", want[i]["kind"], want[i]["metadata"].(map[string]any)["name"])
		var paths []string
		for k := range w {
			paths = append(paths, k)
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				paths = append(paths, k)
			}
		}
		sort.Strings(paths)
		for _, p := range paths {
			if w[p] != g[p] {
				t.Errorf("%s %s:\n  helm:    %s\n  adapter: %s", name, p, orMissing(w, p), orMissing(g, p))
			}
		}
	}
}

func orMissing(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return "(absent)"
}

// definition equal to the chart agent profile-demo (components/governance/profiles-demo-values.yaml).
func profileDemo() *api.AgentDefinition {
	return &api.AgentDefinition{Metadata: api.ObjectMeta{Name: "profile-demo", Namespace: "kagent"}, Spec: api.Spec{
		Description: `Narrow demo agent on the tool profile "lookup"`,
		Model:       "claude-haiku",
		Instructions: "You answer questions about software vulnerabilities and the platform's own documents. Keep answers to two sentences.\n" +
			"You have only the tools that are listed for you.\n",
		ToolProfile: "lookup",
		Tools:       []string{"execute_tool_script", "osv_query_vulnerability", "osv_get_vulnerability", "knowledge_kb_search"},
		Egress:      api.Egress{Allow: []string{"https://api.osv.dev"}},
	}}
}

func TestParityWithHelmProfileDemo(t *testing.T) {
	c := loadCat(t)
	p := plan.Build(profileDemo(), c, tools{c})
	m, err := New(c, nil).Desired(p)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, "../../../testdata/golden-profile-demo.json", m)
}

// The chart's default agent: no profile (shared tool server), knowledge with graph, notify, memory with a ttl, a skill.
// Its tool list and ModelConfig name are set by hand in values, so the plan is adjusted the same way.
func TestParityWithHelmAssistant(t *testing.T) {
	c := loadCat(t)
	d := &api.AgentDefinition{Metadata: api.ObjectMeta{Name: "assistant", Namespace: "kagent"}, Spec: api.Spec{
		Description: "General assistant reachable from messaging channels",
		Model:       "claude-haiku",
		Instructions: "You are a concise, helpful assistant running on a Kubernetes-native agent\n" +
			"platform. Answers are read in a chat app, so keep them short and use plain text.\n" +
			"Use the available tools when they help; say when you used one.\n",
		Tools:     []string{"execute_tool_script", "fetch_fetch", "osv_get_vulnerability", "osv_query_vulnerabilities_batch", "osv_query_vulnerability"},
		Skills:    []string{"openagentix-canary"},
		Knowledge: true, Notify: true,
		Memory: api.Memory{Enabled: true, TTLDays: 30},
	}}
	p := plan.Build(d, c, tools{c})
	p.ModelConfigName = "gateway-claude-haiku" // agents[].modelConfigName in values
	m, err := New(c, nil).Desired(p)
	if err != nil {
		t.Fatal(err)
	}
	compare(t, "../../../testdata/golden-assistant.json", m)
}

func TestOrderAndKinds(t *testing.T) {
	c := loadCat(t)
	m, _ := New(c, nil).Desired(plan.Build(profileDemo(), c, tools{c}))
	var kinds []string
	for _, x := range m {
		kinds = append(kinds, x.Object["kind"].(string))
		if x.Object["apiVersion"] != "api.kagent.dev/v1alpha3" || x.Resource.APIVersion() != "api.kagent.dev/v1alpha3" {
			t.Fatalf("%v", x.Resource)
		}
	}
	if strings.Join(kinds, ",") != "ModelConfig,Harness,AgentTemplate,EgressPolicy,Agent" {
		t.Fatal(kinds)
	}
	if got := len(New(c, nil).OwnedKinds()); got != 5 {
		t.Fatalf("owned kinds %d", got)
	}
}

func TestNoEgressMeansNoPolicyAndNoRef(t *testing.T) {
	c := loadCat(t)
	d := profileDemo()
	d.Spec.Egress = api.Egress{}
	m, _ := New(c, nil).Desired(plan.Build(d, c, tools{c}))
	if len(m) != 4 {
		t.Fatalf("%d objects", len(m))
	}
	spec := m[3].Object["spec"].(map[string]any)
	if _, ok := spec["egressPolicyRefs"]; ok {
		t.Fatal("egressPolicyRefs must be absent")
	}
}

func TestNoToolsMeansNoToolsKey(t *testing.T) {
	c := loadCat(t)
	d := profileDemo()
	d.Spec.Tools, d.Spec.ToolProfile = nil, ""
	m, _ := New(c, nil).Desired(plan.Build(d, c, tools{c}))
	if _, ok := m[2].Object["spec"].(map[string]any)["tools"]; ok {
		t.Fatal("tools must be absent")
	}
}

func TestHarnessEnvFromCatalog(t *testing.T) {
	c := loadCat(t)
	c.HarnessEnv = []catalog.EnvVar{{Name: "OTEL_PROPAGATORS", Value: "tracecontext,baggage"}}
	m, _ := New(c, nil).Desired(plan.Build(profileDemo(), c, tools{c}))
	env := m[1].Object["spec"].(map[string]any)["env"].([]any)
	e := env[0].(map[string]any)
	if e["name"] != "OTEL_PROPAGATORS" || e["value"] != "tracecontext,baggage" {
		t.Fatalf("%v", env)
	}
}

func TestCallEndpointAndRef(t *testing.T) {
	c := loadCat(t)
	p := plan.Build(profileDemo(), c, tools{c})
	r := New(c, nil)
	if got := r.CallEndpoint(p); got != "http://a2a-gate.openagentix-gateway.svc.cluster.local:8080/agents/kagent/profile-demo" {
		t.Fatal(got)
	}
	c.GateURL = ""
	if got := New(c, nil).CallEndpoint(p); got != "" {
		t.Fatalf("no gate, no endpoint: %q", got)
	}
	ref := r.Ref(p)
	if ref != (api.ObjectRef{APIVersion: "api.kagent.dev/v1alpha3", Kind: "Agent", Name: "profile-demo"}) {
		t.Fatalf("%+v", ref)
	}
}

type getter map[string]kube.Object

func (g getter) Get(_ context.Context, r kube.Resource, _, name string) (kube.Object, error) {
	if o, ok := g[r.Plural+"/"+name]; ok {
		return o, nil
	}
	return nil, &kube.Error{Op: "get", Code: 404, Reason: "NotFound"}
}

func TestReadiness(t *testing.T) {
	c := loadCat(t)
	p := plan.Build(profileDemo(), c, tools{c})
	cond := func(typ, status, reason, msg string) kube.Object {
		return kube.Object{"status": kube.Object{"conditions": []any{kube.Object{"type": typ, "status": status, "reason": reason, "message": msg}}}}
	}
	for name, tc := range map[string]struct {
		g     getter
		ready bool
		why   string
	}{
		"missing":    {getter{}, false, "AgentNotFound"},
		"no status":  {getter{"agents/profile-demo": kube.Object{}}, false, "AgentPending"},
		"ready":      {getter{"agents/profile-demo": cond("Ready", "True", "Ready", "")}, true, "Ready"},
		"not ready":  {getter{"agents/profile-demo": cond("Ready", "False", "DeploymentNotReady", "waiting")}, false, "DeploymentNotReady"},
		"unaccepted": {getter{"agents/profile-demo": cond("Accepted", "False", "InvalidTemplate", "bad")}, false, "InvalidTemplate"},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := New(c, tc.g).Readiness(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if r.Ready != tc.ready || r.Reason != tc.why {
				t.Fatalf("%+v", r)
			}
		})
	}
}
