// Package catalog loads the two files the controller is configured with: the platform catalog (what exists) and the
// policy (what a definition may use). Both are JSON, rendered by the chart, and hold no secret. Every field is
// documented in the README.
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
)

// OriginRE is the shape of an egress origin, copied from the chart (oa.agentEgress): scheme, lower case DNS name (a wildcard
// replaces exactly one leftmost label), optional port, no path.
var OriginRE = regexp.MustCompile(`^https?://(\*\.([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\.)+|([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?\.)*)[a-z]([-a-z0-9]{0,61}[a-z0-9])?(:([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5]))?$`)

// MaxOriginLen is the longest origin the chart accepts.
const MaxOriginLen = 270

var digestRE = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

type Adapters struct {
	Runtime      string `json:"runtime"`
	ModelGateway string `json:"modelGateway"`
	Tools        string `json:"tools"`
}

type Namespaces struct {
	Agents  string `json:"agents"`  // where definitions live and objects are written
	Gateway string `json:"gateway"` // where the controller's key ConfigMap lives
	Tools   string `json:"tools"`   // the tool servers' namespace: egress into it is refused
}

// Profile is one tool profile as the agent sees it.
type Profile struct {
	Name       string   `json:"name"`
	ToolServer string   `json:"toolServer"` // name of the tool-server object an agent with this profile uses
	CodeMode   bool     `json:"codeMode"`
	Tools      []string `json:"tools"` // agent-side names the profile offers, execute_tool_script included when codeMode
}

// Feature is knowledge or notify: an optional service that adds tools and a prompt to an agent.
type Feature struct {
	Available bool     `json:"available"`
	Tools     []string `json:"tools"`  // agent-side names, in the order the chart adds them
	Prompt    string   `json:"prompt"` // addition to the system prompt, tool names already substituted
}

type Memory struct {
	Available      bool   `json:"available"`      // the memory stack is installed
	ExpiryEnabled  bool   `json:"expiryEnabled"`  // the job that enforces the time limit runs
	DefaultTTLDays int    `json:"defaultTtlDays"` // the runtime's default when a definition sets none
	ModelConfig    string `json:"modelConfig"`    // name of the shared embedding model object
	Prompt         string `json:"prompt"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Catalog is everything that exists on the platform.
type Catalog struct {
	Adapters         Adapters          `json:"adapters"`
	Namespaces       Namespaces        `json:"namespaces"`
	GatewayBaseURL   string            `json:"gatewayBaseUrl"`
	Models           []string          `json:"models"`
	ToolServer       string            `json:"toolServer"` // the shared tool server, used when a definition has no profile
	Profiles         []Profile         `json:"profiles"`
	Knowledge        Feature           `json:"knowledge"`
	Notify           Feature           `json:"notify"`
	Memory           Memory            `json:"memory"`
	Skills           map[string]string `json:"skills"` // name -> OCI ref pinned by digest
	HarnessImage     string            `json:"harnessImage"`
	WorkerPool       string            `json:"workerPool"`
	SnapshotLocation string            `json:"snapshotLocation"`
	HarnessEnv       []EnvVar          `json:"harnessEnv"`
	GateURL          string            `json:"gateUrl"` // empty when the A2A gate is off
	ReservedNames    []string          `json:"reservedNames"`
}

// Profile returns the profile called name.
func (c *Catalog) Profile(name string) (Profile, bool) {
	for _, p := range c.Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// ProfileNames lists the profiles, for messages.
func (c *Catalog) ProfileNames() []string {
	var n []string
	for _, p := range c.Profiles {
		n = append(n, p.Name)
	}
	return n
}

func (c *Catalog) HasModel(m string) bool { return slices.Contains(c.Models, m) }

// ToolResolver turns a profile name into the tool server and tools it offers. It is what the Tools adapter provides.
type ToolResolver interface {
	Profile(name string) (Profile, bool)
	Profiles() []string
	SharedServer() string
}

func loadJSON(path string, into any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse %s: trailing data after the JSON document", path)
	}
	return nil
}

// LoadCatalog reads and checks the catalog file.
func LoadCatalog(path string) (*Catalog, error) {
	var c Catalog
	if err := loadJSON(path, &c); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("catalog %s: %w", path, err)
	}
	return &c, nil
}

func (c *Catalog) validate() error {
	var errs []error
	need := func(name, v string) {
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	need("adapters.runtime", c.Adapters.Runtime)
	need("adapters.modelGateway", c.Adapters.ModelGateway)
	need("adapters.tools", c.Adapters.Tools)
	need("namespaces.agents", c.Namespaces.Agents)
	need("namespaces.gateway", c.Namespaces.Gateway)
	need("namespaces.tools", c.Namespaces.Tools)
	need("toolServer", c.ToolServer)
	need("harnessImage", c.HarnessImage)
	need("workerPool", c.WorkerPool)
	need("snapshotLocation", c.SnapshotLocation)
	if u, err := url.Parse(c.GatewayBaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, errors.New("gatewayBaseUrl must be an http or https URL"))
	}
	if c.GateURL != "" {
		if u, err := url.Parse(c.GateURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, errors.New("gateUrl must be empty or an http or https URL"))
		}
	}
	if len(c.Models) == 0 {
		errs = append(errs, errors.New("models must list at least one gateway model"))
	}
	seen := map[string]bool{}
	for i, p := range c.Profiles {
		if p.Name == "" || seen[p.Name] {
			errs = append(errs, fmt.Errorf("profiles[%d]: name is empty or used twice", i))
		}
		seen[p.Name] = true
		if p.ToolServer == "" {
			errs = append(errs, fmt.Errorf("profiles[%d] (%s): toolServer is required", i, p.Name))
		}
		if len(p.Tools) == 0 {
			errs = append(errs, fmt.Errorf("profiles[%d] (%s): tools must not be empty", i, p.Name))
		}
		if p.CodeMode != slices.Contains(p.Tools, "execute_tool_script") {
			errs = append(errs, fmt.Errorf("profiles[%d] (%s): execute_tool_script must be in tools exactly when codeMode is true", i, p.Name))
		}
	}
	for _, f := range []struct {
		n string
		f Feature
	}{{"knowledge", c.Knowledge}, {"notify", c.Notify}} {
		if f.f.Available && (len(f.f.Tools) == 0 || strings.TrimSpace(f.f.Prompt) == "") {
			errs = append(errs, fmt.Errorf("%s: available needs tools and prompt", f.n))
		}
	}
	if c.Memory.Available {
		need("memory.modelConfig", c.Memory.ModelConfig)
		need("memory.prompt", c.Memory.Prompt)
		if c.Memory.DefaultTTLDays < 1 {
			errs = append(errs, errors.New("memory.defaultTtlDays must be at least 1"))
		}
	}
	for n, ref := range c.Skills {
		if !digestRE.MatchString(ref) {
			errs = append(errs, fmt.Errorf("skill %q must be a digest-pinned OCI ref (...@sha256:<64 hex>)", n))
		}
	}
	return errors.Join(errs...)
}

// Policy is what the operator allows. A list that is empty allows nothing; the single entry "*" allows everything the
// catalog has.
type PolicyMemory struct {
	Allowed       bool `json:"allowed"`
	MaxTTLDays    int  `json:"maxTtlDays"`
	RequireExpiry bool `json:"requireExpiry"`
}

type PolicyEgress struct {
	AllowedOrigins []string `json:"allowedOrigins"`
}

type Policy struct {
	MaxAgents          int          `json:"maxAgents"`
	AllowedModels      []string     `json:"allowedModels"`
	RequireToolProfile bool         `json:"requireToolProfile"`
	AllowedProfiles    []string     `json:"allowedProfiles"`
	AllowKnowledge     bool         `json:"allowKnowledge"`
	AllowNotify        bool         `json:"allowNotify"`
	AllowedSkills      []string     `json:"allowedSkills"`
	Memory             PolicyMemory `json:"memory"`
	Egress             PolicyEgress `json:"egress"`
	ReservedNames      []string     `json:"reservedNames"`
}

func allowed(list []string, v string) bool {
	return slices.Contains(list, "*") || slices.Contains(list, v)
}

func (p *Policy) ModelAllowed(m string) bool   { return allowed(p.AllowedModels, m) }
func (p *Policy) ProfileAllowed(n string) bool { return allowed(p.AllowedProfiles, n) }
func (p *Policy) SkillAllowed(n string) bool   { return allowed(p.AllowedSkills, n) }

// LoadPolicy reads and checks the policy file.
func LoadPolicy(path string) (*Policy, error) {
	var p Policy
	if err := loadJSON(path, &p); err != nil {
		return nil, err
	}
	var errs []error
	if p.MaxAgents < 0 {
		errs = append(errs, errors.New("maxAgents must not be negative"))
	}
	if p.Memory.MaxTTLDays < 0 {
		errs = append(errs, errors.New("memory.maxTtlDays must not be negative"))
	}
	if p.Memory.Allowed && p.Memory.MaxTTLDays < 1 {
		errs = append(errs, errors.New("memory.maxTtlDays must be at least 1 when memory.allowed is true"))
	}
	for _, o := range p.Egress.AllowedOrigins {
		if !OriginRE.MatchString(o) || len(o) > MaxOriginLen {
			errs = append(errs, fmt.Errorf("egress.allowedOrigins entry %q is not an origin (https://host[:port], one leading *. wildcard allowed)", o))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	return &p, nil
}
