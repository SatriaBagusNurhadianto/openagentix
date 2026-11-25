package guardtest

import (
	"os"
	"strings"
	"testing"

	v1 "k8s.io/api/admissionregistration/v1"
)

func loadEgress(t *testing.T) egressTable {
	var eg egressTable
	readJSON(t, "services/control-plane/testdata/egress-hosts.json", &eg)
	return eg
}

// runCases evaluates every request and checks the verdict. It returns the numbers of allowed and denied expectations.
func runCases(t *testing.T, e *engine, cases []expectation) (allowed, denied int) {
	t.Helper()
	for _, c := range cases {
		if c.allow {
			allowed++
		} else {
			denied++
		}
		c := c
		t.Run(c.req.Name, func(t *testing.T) {
			v := e.eval(c.req)
			switch {
			case c.allow && !v.Allowed:
				t.Errorf("the guard REFUSED a write it must allow: %v", v.Why)
			case !c.allow && v.Allowed:
				t.Errorf("the guard ALLOWED a write it must refuse (matched policies: %v)", v.Matched)
			}
			if !c.allow && !v.Allowed && !v.Rule {
				t.Errorf("refused only by an evaluation error, no rule said no: %v", v.Why)
			}
			if !c.allow && !v.Allowed && os.Getenv("GUARD_WHY") != "" {
				t.Logf("WHY %s => %v", c.req.Name, v.Why)
			}
			if c.req.User == controller && !c.unmatched && len(v.Matched) == 0 {
				t.Errorf("no policy of the guard looked at a write by the controller: it is not covered")
			}
			if c.unmatched && len(v.Matched) != 0 {
				t.Errorf("this request is meant to be outside the guard, but %v matched it", v.Matched)
			}
		})
	}
	return
}

// TestGuard renders the chart with the demo values and judges every request by evaluating the rendered policy.
func TestGuard(t *testing.T) {
	r := render(t)
	e, err := newEngine(r.Policies)
	if err != nil {
		t.Fatal(err)
	}
	eg := loadEgress(t)
	cases := buildCases(t, r, eg)
	allowed, denied := runCases(t, e, cases)
	t.Logf("guard: %d policies, %d cases (%d must be allowed, %d must be refused)", len(r.Policies), len(cases), allowed, denied)
	if allowed < 30 || denied < 150 {
		t.Errorf("the suite shrank: %d allowed and %d refused cases", allowed, denied)
	}
}

// TestEveryFieldTheReviewNamed fails with a readable list when one of the fields of the security review is not refused.
// (The subtests of TestGuard hold the same checks one by one; this one is the short list to read.)
func TestEveryFieldTheReviewNamed(t *testing.T) {
	r := render(t)
	e, err := newEngine(r.Policies)
	if err != nil {
		t.Fatal(err)
	}
	named := []string{
		"deny EgressPolicy: authentication.secretKeyRef (a Secret's value sent to the origin)",
		"deny Harness: workload.command", "deny Harness: workload.args", "deny Harness: spec.byo", "deny Harness: spec.claude", "deny Harness: spec.codex",
		"deny Harness: kagent.compaction.summarizer.modelConfigRef",
		"deny ModelConfig: a model that is not in the catalog", "deny ModelConfig: defaultHeaders", "deny ModelConfig: tls.disableVerify",
		"deny ModelConfig: actorJwt", "deny ModelConfig: tokenPassthrough", "deny ModelConfig: allowedNamespaces",
		"deny AgentTemplate: plugins", "deny AgentTemplate: promptTemplate.dataSources", "deny AgentTemplate: systemPromptFrom",
		"deny AgentTemplate: outputSchemaFrom", "deny AgentTemplate: modelConfig.namespace", "deny AgentTemplate: tools[].mcp.server.namespace",
		"deny AgentTemplate: tools[].mcp.requireApproval", "deny AgentTemplate: tools[].mcp.tools a tool the profile does not offer",
		"deny ConfigMap: another gateway user id (a foreign prefix)", "deny ConfigMap: user_id differs from the key",
	}
	want := map[string]bool{}
	for _, n := range named {
		want[n] = true
	}
	var bad []string
	seen := 0
	for _, c := range buildCases(t, r, loadEgress(t)) {
		if !want[c.req.Name] {
			continue
		}
		seen++
		if v := e.eval(c.req); v.Allowed {
			bad = append(bad, c.req.Name)
		}
	}
	if seen != len(named) {
		t.Fatalf("the list names %d cases, the suite has %d of them", len(named), seen)
	}
	if len(bad) > 0 {
		t.Errorf("%d field(s) the security review named are NOT refused:\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
}

// TestInClusterListCannotOpenForbiddenHosts widens the rendered list of operator-listed destinations by hand (the chart refuses to
// render such a list) and checks that the guard still refuses the Kubernetes API, kube-system and the platform's namespaces.
func TestInClusterListCannotOpenForbiddenHosts(t *testing.T) {
	r := render(t)
	eg := loadEgress(t)
	const listed = `["registry.ci.svc:5000"]`
	widened := `["registry.ci.svc:5000","` + strings.Join(eg.ForbiddenInCluster, `","`) + `"]`
	patched := 0
	var pols []v1.ValidatingAdmissionPolicy
	for _, p := range r.Policies {
		p = *p.DeepCopy()
		for i := range p.Spec.Validations {
			if strings.Contains(p.Spec.Validations[i].Expression, listed) {
				p.Spec.Validations[i].Expression = strings.ReplaceAll(p.Spec.Validations[i].Expression, listed, widened)
				patched++
			}
		}
		for i := range p.Spec.Variables {
			if strings.Contains(p.Spec.Variables[i].Expression, listed) {
				p.Spec.Variables[i].Expression = strings.ReplaceAll(p.Spec.Variables[i].Expression, listed, widened)
				patched++
			}
		}
		pols = append(pols, p)
	}
	if patched == 0 {
		t.Fatalf("the rendered guard does not hold the list %s", listed)
	}
	e, err := newEngine(pols)
	if err != nil {
		t.Fatal(err)
	}
	A := fromGolden(t, "golden-profile-demo.json", "profile-demo", "researcher", "gateway-profile-demo")
	for _, hp := range eg.ForbiddenInCluster {
		host, port, _ := strings.Cut(hp, ":")
		scheme := "https"
		if port == "80" {
			scheme = "http"
		}
		x := find(A, "EgressPolicy")
		put(x, "spec/destinations", []any{m{"origin": scheme + "://" + host + ":" + port}})
		v := e.eval(request{Name: hp, User: controller, Op: "CREATE", Group: "api.kagent.dev", Resource: "egresspolicies", Namespace: "kagent", ObjName: "researcher-egress", Object: x})
		if v.Allowed {
			t.Errorf("the destination %s is listed by the operator and the guard allowed it: it can never be opened", hp)
		}
	}
}

// TestPoliciesAreWiredSafely checks the objects around the expressions: every policy fails closed, only looks at the controller,
// has a Deny binding and a message on each validation.
func TestPoliciesAreWiredSafely(t *testing.T) {
	r := render(t)
	bound := map[string]bool{}
	for _, b := range r.Bindings {
		if len(b.Spec.ValidationActions) != 1 || b.Spec.ValidationActions[0] != v1.Deny {
			t.Errorf("binding %s must have exactly the action Deny", b.Name)
		}
		bound[b.Spec.PolicyName] = true
	}
	for _, p := range r.Policies {
		if !bound[p.Name] {
			t.Errorf("policy %s has no binding", p.Name)
		}
		if p.Spec.FailurePolicy == nil || *p.Spec.FailurePolicy != v1.Fail {
			t.Errorf("policy %s must have failurePolicy Fail", p.Name)
		}
		if len(p.Spec.MatchConditions) == 0 || !strings.Contains(p.Spec.MatchConditions[0].Expression, "system:serviceaccount:"+releaseNamespace+":openagentix-control-plane") {
			t.Errorf("policy %s must be limited to the controller's ServiceAccount", p.Name)
		}
		for _, v := range p.Spec.Validations {
			if v.Message == "" {
				t.Errorf("policy %s: a validation has no message", p.Name)
			}
		}
		if mc := p.Spec.MatchConstraints; mc != nil {
			for _, rr := range mc.ResourceRules {
				for _, op := range rr.Operations {
					if op == v1.Connect {
						t.Errorf("policy %s matches CONNECT", p.Name)
					}
				}
			}
		}
	}
	for n := range bound {
		found := false
		for _, p := range r.Policies {
			found = found || p.Name == n
		}
		if !found {
			t.Errorf("binding for a policy that is not rendered: %s", n)
		}
	}
}
