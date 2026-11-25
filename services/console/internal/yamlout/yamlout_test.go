package yamlout

import (
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/console/internal/model"
)

func TestScalarQuoting(t *testing.T) {
	for in, want := range map[string]string{
		"claude-haiku": "claude-haiku", "": `""`, "true": `"true"`, "a: b": `"a: b"`, "https://api.osv.dev": `"https://api.osv.dev"`, "3": `"3"`,
	} {
		if got := Scalar(in); got != want {
			t.Errorf("Scalar(%q)=%s want %s", in, got, want)
		}
	}
}

func TestRender(t *testing.T) {
	out := Render(Meta{Name: "researcher", Namespace: "kagent", Generation: 3},
		model.Spec{Model: "claude-haiku", Instructions: "line one\nline two\n", ToolProfile: "lookup", Tools: []string{"osv_query_vulnerability"},
			Memory: &model.Memory{Enabled: true, TTLDays: 15}, Egress: &model.Egress{Allow: []string{"https://api.osv.dev"}},
			Knowledge: &model.Feature{Enabled: true}, Skills: []model.Skill{{Name: "s", Version: "1"}}, Suspend: true},
		&model.Status{ObservedGeneration: 3, Conditions: []model.Condition{{Type: "Ready", Status: "True", Reason: "Ready"}}})
	for _, want := range []string{
		"apiVersion: openagentix.io/v1alpha1\n", "  name: researcher\n", "  generation: 3\n", "  instructions: |\n    line one\n    line two\n",
		"  tools:\n    - osv_query_vulnerability\n", "    ttlDays: 15\n", "  knowledge:\n    enabled: true\n", "  skills:\n    - name: s\n      version: \"1\"\n", "  suspend: true\n", "    allow:\n      - \"https://api.osv.dev\"\n", "    - type: Ready\n      status: \"True\"\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
