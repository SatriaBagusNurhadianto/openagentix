package policy

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
)

// The example definition shipped in components/ must decode strictly into the API types (a field of an old shape would be an
// unknown field here) and be accepted by the catalog and policy examples. Skipped without yq.
func TestTheExampleDefinitionIsAcceptedAndHasTheCurrentShape(t *testing.T) {
	if _, err := exec.LookPath("yq"); err != nil {
		t.Skip("yq not installed")
	}
	path := "../../../../components/control-plane/researcher.yaml"
	if _, err := os.Stat(path); err != nil {
		t.Skip("example not found")
	}
	out, err := exec.Command("yq", "-c", ".", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var d api.AgentDefinition
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		t.Fatalf("the example does not match the API types: %v", err)
	}
	if d.APIVersion != api.APIVersion || d.Kind != api.Kind || d.Metadata.Namespace == "" {
		t.Fatalf("%+v", d)
	}
	c, p := load(t)
	if v := Validate(Input{Def: &d, Catalog: c, Policy: p, Tools: fakeTools{c}}); len(v) != 0 {
		t.Fatalf("the example is refused: %q", v)
	}
	if d.Metadata.Labels[api.LabelTeam] == "" {
		t.Fatal("the example shows the reserved labels")
	}
}
