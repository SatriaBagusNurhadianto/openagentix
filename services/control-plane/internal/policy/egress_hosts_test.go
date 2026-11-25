package policy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
)

// egressTable is testdata/egress-hosts.json: the same table drives the guard's CEL test (hack/ci/guardtest), so the Go check
// and the admission policy cannot disagree about a host (an origin the Go check accepts and the guard refuses would be
// Accepted, then ApplyFailed forever).
type egressTable struct {
	Namespaces         struct{ Agents, Gateway, Tools string }
	InCluster          []string
	ForbiddenInCluster []string
	Cases              []struct {
		Origin string
		Allow  bool
		Why    string
	}
}

func loadEgressTable(t *testing.T) egressTable {
	t.Helper()
	b, err := os.ReadFile("../../testdata/egress-hosts.json")
	if err != nil {
		t.Fatal(err)
	}
	var tb egressTable
	if err := json.Unmarshal(b, &tb); err != nil {
		t.Fatal(err)
	}
	return tb
}

// TestEgressHostTable: for every origin of the shared table, the definition is accepted exactly when the table says allow.
// The policy lists the origin itself as allowed, so only the host rules decide.
func TestEgressHostTable(t *testing.T) {
	tb := loadEgressTable(t)
	if len(tb.Cases) < 50 {
		t.Fatalf("the table shrank to %d cases", len(tb.Cases))
	}
	for _, c := range tb.Cases {
		t.Run(c.Origin+" ("+c.Why+")", func(t *testing.T) {
			v := run(t, func(d *api.AgentDefinition, cat *catalog.Catalog, p *catalog.Policy) {
				cat.Namespaces.Agents, cat.Namespaces.Gateway, cat.Namespaces.Tools = tb.Namespaces.Agents, tb.Namespaces.Gateway, tb.Namespaces.Tools
				d.Spec.Egress.Allow = []string{c.Origin}
				p.Egress.AllowedOrigins = []string{c.Origin}
				p.Egress.InClusterDestinations = tb.InCluster
			})
			var egress []string
			for _, s := range v {
				if strings.Contains(s, "egress.allow") {
					egress = append(egress, s)
				}
			}
			if c.Allow && len(egress) > 0 {
				t.Errorf("the Go check refused an origin the table allows: %v", egress)
			}
			if !c.Allow && len(egress) == 0 {
				t.Errorf("the Go check accepted an origin the table refuses")
			}
		})
	}
}
