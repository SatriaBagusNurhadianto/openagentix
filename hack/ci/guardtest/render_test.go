package guardtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	v1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// repoRoot is the root of the repository (this file is hack/ci/guardtest/render_test.go).
func repoRoot() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(f), "..", "..", ".."))
}

// demoValues are the values files install.sh combines for the demo (its header comment), the control plane's last.
var demoValues = []string{
	"components/channels/demo-values.yaml",
	"components/schedules/demo-values.yaml",
	"components/notify/demo-values.yaml",
	"components/observability/demo-values.yaml",
	"components/knowledge/graph-demo-values.yaml",
	"components/governance/demo-values.yaml",
	"components/governance/profiles-demo-values.yaml",
	"components/governance/memory-demo-values.yaml",
	"components/control-plane/demo-values.yaml",
}

// The controller's own settings the guard cases rely on, on top of the demo values: one in-cluster destination the operator
// lists (the guard must allow exactly it) and memory allowed (the golden assistant has memory).
var extraSets = []string{
	"controlPlane.policy.egress.inClusterDestinations={registry.ci.svc:5000}",
	"controlPlane.policy.memory.allowed=true",
	"observability.baggage=true", // the catalog then lists one harness env entry (OTEL_PROPAGATORS)
}

const releaseNamespace = "openagentix-system"

type rendered struct {
	docs     []map[string]any
	Policies []v1.ValidatingAdmissionPolicy
	Bindings []v1.ValidatingAdmissionPolicyBinding
	Catalog  catalogFile
}

type catalogFile struct {
	Namespaces struct{ Agents, Gateway, Tools string }
	Models     []string
	Skills     map[string]string
	Runtime    struct {
		Kagent struct {
			HarnessImage string `json:"harnessImage"`
			HarnessEnv   []struct{ Name, Value string }
		}
	}
	Tools struct {
		Toolhive struct {
			Profiles []struct {
				Name       string
				ToolServer string
				Tools      []string
			}
		}
	}
}

// render runs `helm template` with the demo values (and the sets) and returns the policy objects and the catalog.
func render(t *testing.T, extra ...string) *rendered {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	root := repoRoot()
	args := []string{"template", "openagentix", filepath.Join(root, "charts/openagentix"), "-n", releaseNamespace}
	for _, f := range demoValues {
		args = append(args, "-f", filepath.Join(root, f))
	}
	for _, s := range append(append([]string{}, extraSets...), extra...) {
		args = append(args, "--set", s)
	}
	var out, errb bytes.Buffer
	cmd := exec.Command("helm", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, errb.String())
	}
	r := &rendered{}
	dec := yaml.NewYAMLOrJSONDecoder(&out, 4096)
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode helm output: %v", err)
		}
		if len(m) == 0 {
			continue
		}
		r.docs = append(r.docs, m)
		b, _ := json.Marshal(m)
		switch m["kind"] {
		case "ValidatingAdmissionPolicy":
			var p v1.ValidatingAdmissionPolicy
			if err := json.Unmarshal(b, &p); err != nil {
				t.Fatal(err)
			}
			r.Policies = append(r.Policies, p)
		case "ValidatingAdmissionPolicyBinding":
			var p v1.ValidatingAdmissionPolicyBinding
			if err := json.Unmarshal(b, &p); err != nil {
				t.Fatal(err)
			}
			r.Bindings = append(r.Bindings, p)
		case "ConfigMap":
			md, _ := m["metadata"].(map[string]any)
			if md["name"] == "openagentix-control-plane-catalog" {
				data, _ := m["data"].(map[string]any)
				s, _ := data["catalog.json"].(string)
				if err := json.Unmarshal([]byte(s), &r.Catalog); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if len(r.Policies) == 0 {
		t.Fatal("the render has no ValidatingAdmissionPolicy: is the control plane on?")
	}
	return r
}

func readJSON(t *testing.T, rel string, into any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(), rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
}
