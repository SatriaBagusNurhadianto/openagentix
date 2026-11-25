// Package kagent is the Runtime adapter for kagent 1.0.1 with Agent Substrate. It turns a Plan into the five objects
// that charts/openagentix/templates/kagent.yaml renders for a chart agent (ModelConfig, Harness, AgentTemplate, optional
// EgressPolicy, Agent), field for field; a golden test compares it with `helm template`. Labels and owner references are
// not set here: the core stamps them on every object.
package kagent

import (
	"context"
	"fmt"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
	"github.com/openagentix/openagentix/services/control-plane/internal/plan"
)

const apiVersion = "api.kagent.dev/v1alpha3"

var (
	modelConfigs   = kube.Resource{Group: "api.kagent.dev", Version: "v1alpha3", Plural: "modelconfigs", Kind: "ModelConfig", Namespaced: true}
	harnesses      = kube.Resource{Group: "api.kagent.dev", Version: "v1alpha3", Plural: "harnesses", Kind: "Harness", Namespaced: true}
	agentTemplates = kube.Resource{Group: "api.kagent.dev", Version: "v1alpha3", Plural: "agenttemplates", Kind: "AgentTemplate", Namespaced: true}
	egressPolicies = kube.Resource{Group: "api.kagent.dev", Version: "v1alpha3", Plural: "egresspolicies", Kind: "EgressPolicy", Namespaced: true}
	agents         = kube.Resource{Group: "api.kagent.dev", Version: "v1alpha3", Plural: "agents", Kind: "Agent", Namespaced: true}
)

// Getter reads one object, for readiness.
type Getter interface {
	Get(ctx context.Context, r kube.Resource, ns, name string) (kube.Object, error)
}

type Adapter struct {
	cat *catalog.Catalog
	get Getter
}

// New builds the adapter. get may be nil when only Desired is used.
func New(c *catalog.Catalog, get Getter) *Adapter { return &Adapter{cat: c, get: get} }

func (a *Adapter) Name() string { return "kagent" }

// OwnedKinds are the endpoints the core lists (by label) to prune what is no longer wanted.
func (a *Adapter) OwnedKinds() []kube.Resource {
	return []kube.Resource{modelConfigs, harnesses, agentTemplates, egressPolicies, agents}
}

func obj(kind, ns, name string, spec kube.Object) kube.Object {
	return kube.Object{"apiVersion": apiVersion, "kind": kind, "metadata": kube.Object{"name": name, "namespace": ns}, "spec": spec}
}

func strs(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// Desired returns the objects for a plan, in the chart's order.
func (a *Adapter) Desired(p plan.Plan) ([]kube.Manifest, error) {
	ns := p.Namespace
	var out []kube.Manifest

	mc := obj("ModelConfig", ns, p.ModelConfigName, kube.Object{
		"provider":        "OpenAI",
		"model":           p.Model,
		"apiKeySecret":    p.KeySecret,
		"apiKeySecretKey": p.KeySecretKey,
		"stream":          false,
		"openAI":          kube.Object{"baseUrl": p.GatewayBaseURL},
	})
	mc["metadata"].(kube.Object)["annotations"] = kube.Object{"openagentix.io/agent": p.Name}
	out = append(out, kube.Manifest{Resource: modelConfigs, Object: mc})

	hk := kube.Object{}
	if p.Memory != nil {
		mem := kube.Object{"modelConfigRef": kube.Object{"name": p.Memory.ModelConfig, "namespace": p.Memory.Namespace}}
		if p.Memory.TTLDays > 0 {
			mem["ttlDays"] = p.Memory.TTLDays
		}
		hk["memory"] = mem
	}
	hspec := kube.Object{
		"kagent":    hk,
		"workload":  kube.Object{"image": a.cat.HarnessImage},
		"substrate": kube.Object{"workerPoolRef": kube.Object{"name": a.cat.WorkerPool}, "snapshotPolicy": kube.Object{"location": a.cat.SnapshotLocation}},
	}
	if len(p.HarnessEnv) > 0 {
		var env []any
		for _, e := range p.HarnessEnv {
			env = append(env, kube.Object{"name": e.Name, "value": e.Value})
		}
		hspec["env"] = env
	}
	out = append(out, kube.Manifest{Resource: harnesses, Object: obj("Harness", ns, p.Name+"-harness", hspec)})

	tspec := kube.Object{"modelConfig": kube.Object{"name": p.ModelConfigName}, "systemPrompt": p.Prompt}
	if p.Description != "" {
		tspec["description"] = p.Description
	}
	if len(p.Tools) > 0 {
		tspec["tools"] = []any{kube.Object{"mcp": kube.Object{
			"server": kube.Object{"kind": "RemoteMCPServer", "name": p.ToolServer}, // no apiGroup: kagent's admission webhook rejects it
			"tools":  strs(p.Tools),
		}}}
	}
	if len(p.Skills) > 0 {
		var sk []any
		for _, s := range p.Skills {
			sk = append(sk, kube.Object{"name": s.Name, "source": kube.Object{"oci": s.Ref}})
		}
		tspec["skills"] = sk
	}
	out = append(out, kube.Manifest{Resource: agentTemplates, Object: obj("AgentTemplate", ns, p.Name+"-template", tspec)})

	aspec := kube.Object{"template": kube.Object{"ref": kube.Object{"name": p.Name + "-template"}}, "harnessRef": kube.Object{"name": p.Name + "-harness"}}
	if len(p.Egress) > 0 {
		var d []any
		for _, o := range p.Egress {
			d = append(d, kube.Object{"origin": o})
		}
		out = append(out, kube.Manifest{Resource: egressPolicies, Object: obj("EgressPolicy", ns, p.Name+"-egress", kube.Object{"destinations": d})})
		aspec["egressPolicyRefs"] = []any{kube.Object{"name": p.Name + "-egress"}}
	}
	out = append(out, kube.Manifest{Resource: agents, Object: obj("Agent", ns, p.Name, aspec)})
	return out, nil
}

// Ref is the generic reference status carries.
func (a *Adapter) Ref(p plan.Plan) api.ObjectRef {
	return api.ObjectRef{APIVersion: apiVersion, Kind: "Agent", Name: p.Name}
}

// CallEndpoint is the gate's A2A route for the agent; empty when the gate is off (then nobody but the platform's own
// callers may reach the agent, and the controller does not advertise a route).
func (a *Adapter) CallEndpoint(p plan.Plan) string {
	if a.cat.GateURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/agents/%s/%s", a.cat.GateURL, p.Namespace, p.Name)
}

// Readiness reads the Agent's own conditions.
func (a *Adapter) Readiness(ctx context.Context, p plan.Plan) (plan.Readiness, error) {
	o, err := a.get.Get(ctx, agents, p.Namespace, p.Name)
	if kube.IsNotFound(err) {
		return plan.Readiness{Reason: "AgentNotFound", Message: "the runtime has not created the agent yet"}, nil
	}
	if err != nil {
		return plan.Readiness{}, err
	}
	st, _ := o["status"].(map[string]any)
	conds, _ := st["conditions"].([]any)
	find := func(t string) map[string]any {
		for _, c := range conds {
			if m, ok := c.(map[string]any); ok && m["type"] == t {
				return m
			}
		}
		return nil
	}
	word := func(m map[string]any, def string) string {
		if r, _ := m["reason"].(string); r != "" {
			return r
		}
		return def
	}
	msg := func(m map[string]any) string {
		s, _ := m["message"].(string)
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	}
	if r := find("Ready"); r != nil {
		if r["status"] == "True" {
			return plan.Readiness{Ready: true, Reason: "Ready"}, nil
		}
		return plan.Readiness{Reason: word(r, "NotReady"), Message: msg(r)}, nil
	}
	if ac := find("Accepted"); ac != nil && ac["status"] == "False" {
		return plan.Readiness{Reason: word(ac, "NotAccepted"), Message: msg(ac)}, nil
	}
	return plan.Readiness{Reason: "AgentPending", Message: "the runtime reports no Ready condition yet"}, nil
}
