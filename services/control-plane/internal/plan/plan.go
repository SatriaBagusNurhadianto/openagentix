// Package plan turns an accepted definition into a Plan: the platform-level facts an agent needs (final prompt, tool
// server and tools, skills with digests, egress, memory), with no upstream vocabulary. Runtime adapters translate a Plan
// into their own objects. Validation happens before: Build assumes the definition passed package policy.
package plan

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
)

// KeySecretKey is the data key of the virtual-key Secret; the chart's agent Secrets use the same one.
const KeySecretKey = "OPENAI_API_KEY"

var hex8 = regexp.MustCompile(`^[0-9a-f]{8}`)

// KeySecretName is "<name>-virtual-key-" and the first 8 hex characters of the definition's UID without dashes. A name
// that already exists therefore belongs to this very definition (a definition that is deleted and made again has a new
// UID), so the controller may replace it without reading it.
func KeySecretName(name, uid string) (string, error) {
	h := hex8.FindString(strings.ReplaceAll(strings.ToLower(uid), "-", ""))
	if h == "" {
		return "", fmt.Errorf("definition %s has no usable UID (need 8 hex characters), cannot name its key Secret", name)
	}
	return name + "-virtual-key-" + h, nil
}

type Skill struct{ Name, Ref string }

type Memory struct {
	TTLDays     int // 0: the runtime's default
	ModelConfig string
	Namespace   string
}

type Plan struct {
	Name            string
	Namespace       string
	Description     string
	Model           string
	GatewayBaseURL  string
	GatewayUserID   string
	ModelConfigName string
	KeySecret       string
	KeySecretKey    string
	// KeyFingerprint is a non-secret fingerprint of the registered key hash. A runtime adapter puts it on the objects that
	// must be recreated when the key is replaced. Empty until the key step has run.
	KeyFingerprint string
	Prompt         string
	ToolServer     string // "" when the agent has no tools
	Tools          []string
	Skills         []Skill
	Egress         []string
	Memory         *Memory
	HarnessEnv     []catalog.EnvVar
}

// Build composes the plan. The prompt follows the chart: with no addition the instructions pass through unchanged,
// otherwise each part is trimmed and joined by a newline, in the order knowledge, notify, memory.
func Build(d *api.AgentDefinition, c *catalog.Catalog, tr catalog.ToolResolver) (Plan, error) {
	s := d.Spec
	secret, err := KeySecretName(d.Metadata.Name, d.Metadata.UID)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{
		Name: d.Metadata.Name, Namespace: c.Namespaces.Agents, Description: s.Description, Model: s.Model,
		GatewayBaseURL: c.GatewayBaseURL, GatewayUserID: d.Metadata.Name,
		ModelConfigName: "gateway-" + d.Metadata.Name,
		KeySecret:       secret, KeySecretKey: KeySecretKey,
		Prompt:     s.Instructions,
		Egress:     slices.Clone(s.Egress.Allow),
		HarnessEnv: slices.Clone(c.HarnessEnv),
	}
	add := func(extra string) { p.Prompt = strings.TrimSpace(p.Prompt) + "\n" + strings.TrimSpace(extra) }
	tools := slices.Clone(s.Tools)
	extend := func(names []string) {
		for _, n := range names {
			if !slices.Contains(tools, n) {
				tools = append(tools, n)
			}
		}
	}
	if s.Knowledge {
		add(c.Knowledge.Prompt)
		extend(c.Knowledge.Tools)
	}
	if s.Notify {
		add(c.Notify.Prompt)
		extend(c.Notify.Tools)
	}
	if s.Memory.Enabled {
		add(c.Memory.Prompt)
		p.Memory = &Memory{TTLDays: s.Memory.TTLDays, ModelConfig: c.Memory.ModelConfig, Namespace: c.Namespaces.Agents}
	}
	p.Tools = tools
	if len(tools) > 0 {
		if s.ToolProfile == "" {
			return Plan{}, fmt.Errorf("definition %s has tools but no toolProfile: the shared tool server is not an option", d.Metadata.Name)
		}
		prof, ok := tr.Profile(s.ToolProfile)
		if !ok {
			return Plan{}, fmt.Errorf("definition %s names toolProfile %q, which the tools adapter does not know", d.Metadata.Name, s.ToolProfile)
		}
		p.ToolServer = prof.ToolServer
	}
	for _, n := range s.Skills {
		p.Skills = append(p.Skills, Skill{Name: n, Ref: c.Skills[n]})
	}
	return p, nil
}

// Readiness is what a runtime reports about a running agent.
type Readiness struct {
	Ready   bool
	Reason  string // one CamelCase word
	Message string // short, from the runtime's own status; no secret can be in it
}
