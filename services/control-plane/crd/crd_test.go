package crd

import (
	"encoding/json"
	"os/exec"
	"testing"
)

// The CRD is YAML and the module has no YAML parser, so the test converts it with yq (a dependency of the repo's other
// tests, charts/openagentix/tests/run.sh) and skips when yq is missing.
func load(t *testing.T) map[string]any {
	t.Helper()
	if _, err := exec.LookPath("yq"); err != nil {
		t.Skip("yq not installed")
	}
	out, err := exec.Command("yq", "-c", ".", "agentdefinition.yaml").Output()
	if err != nil {
		t.Fatalf("yq: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func dig(t *testing.T, m any, path ...string) any {
	t.Helper()
	for _, p := range path {
		mm, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("not a map at %q", p)
		}
		m, ok = mm[p]
		if !ok {
			t.Fatalf("missing %q in path %v", p, path)
		}
	}
	return m
}

func TestIdentityAndSubresource(t *testing.T) {
	c := load(t)
	spec := dig(t, c, "spec").(map[string]any)
	n := spec["names"].(map[string]any)
	if spec["group"] != "openagentix.io" || spec["scope"] != "Namespaced" || n["kind"] != "AgentDefinition" || n["plural"] != "agentdefinitions" || n["shortNames"].([]any)[0] != "oad" {
		t.Fatalf("%v", spec)
	}
	v := spec["versions"].([]any)[0].(map[string]any)
	if v["name"] != "v1alpha1" || v["served"] != true || v["storage"] != true {
		t.Fatal(v)
	}
	if _, ok := dig(t, v, "subresources").(map[string]any)["status"]; !ok {
		t.Fatal("status subresource")
	}
	var cols []string
	for _, c := range v["additionalPrinterColumns"].([]any) {
		cols = append(cols, c.(map[string]any)["name"].(string))
	}
	if len(cols) != 5 || cols[0] != "Model" || cols[1] != "Profile" || cols[2] != "Accepted" || cols[3] != "Ready" || cols[4] != "Age" {
		t.Fatalf("%v", cols)
	}
}

func TestLimitsOfTheADR(t *testing.T) {
	c := load(t)
	v := dig(t, c, "spec", "versions").([]any)[0]
	props := dig(t, v, "schema", "openAPIV3Schema", "properties", "spec", "properties")
	num := func(path ...string) float64 { return dig(t, props, path...).(float64) }
	if num("description", "maxLength") != 256 {
		t.Fatal("description")
	}
	if num("instructions", "maxLength") != 16384 || num("instructions", "minLength") != 1 {
		t.Fatal("instructions")
	}
	if num("memory", "properties", "ttlDays", "minimum") != 1 {
		t.Fatal("ttlDays")
	}
	if num("egress", "properties", "allow", "maxItems") != 64 {
		t.Fatal("egress.allow")
	}
	req := dig(t, v, "schema", "openAPIV3Schema", "properties", "spec", "required").([]any)
	if len(req) != 2 || req[0] != "model" || req[1] != "instructions" {
		t.Fatalf("required %v", req)
	}
	rules := dig(t, v, "schema", "openAPIV3Schema", "x-kubernetes-validations").([]any)
	if len(rules) != 2 {
		t.Fatal("name rules")
	}
}

func TestSpecHasNoBackdoorFields(t *testing.T) {
	c := load(t)
	v := dig(t, c, "spec", "versions").([]any)[0]
	props := dig(t, v, "schema", "openAPIV3Schema", "properties", "spec", "properties").(map[string]any)
	for _, f := range []string{"modelConfig", "modelConfigRef", "harness", "harnessImage", "image", "remoteMCPServer", "toolServer", "egressPolicyRef", "namespace", "callers", "gatewayUserId", "budget"} {
		if _, ok := props[f]; ok {
			t.Fatalf("spec must not have %q", f)
		}
	}
}
