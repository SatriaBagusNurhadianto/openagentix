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
	"net"
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

// Feature is knowledge or notify: an optional service that adds tools and a prompt to an agent.
type Feature struct {
	Available bool     `json:"available"`
	Tools     []string `json:"tools"`  // agent-side names, in the order the chart adds them
	Prompt    string   `json:"prompt"` // addition to the system prompt, tool names already substituted
}

// Memory is the platform-level fact about long-term memory. The runtime adapter's own settings (for example the shared
// embedding model object) are in the adapter's section.
type Memory struct {
	Available      bool   `json:"available"`      // the memory stack is installed
	ExpiryEnabled  bool   `json:"expiryEnabled"`  // the job that enforces the time limit runs
	DefaultTTLDays int    `json:"defaultTtlDays"` // the runtime's default when a definition sets none
	Prompt         string `json:"prompt"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// KagentSettings is the runtime adapter's own section of the catalog (runtime.kagent). Nothing outside the adapter reads it.
type KagentSettings struct {
	HarnessImage      string   `json:"harnessImage"`
	WorkerPool        string   `json:"workerPool"`
	SnapshotLocation  string   `json:"snapshotLocation"`
	HarnessEnv        []EnvVar `json:"harnessEnv"`        // OTEL_PROPAGATORS when observability and baggage are on, else []
	MemoryModelConfig string   `json:"memoryModelConfig"` // name of the shared embedding model config
}

// RuntimeSections holds one section per runtime adapter; the one named in adapters.runtime must be present.
type RuntimeSections struct {
	Kagent *KagentSettings `json:"kagent"`
}

// ToolhiveProfile is one tool profile in the tools adapter's own section.
type ToolhiveProfile struct {
	Name       string   `json:"name"`
	ToolServer string   `json:"toolServer"` // name of the tool-server object an agent with this profile uses
	CodeMode   bool     `json:"codeMode"`
	Tools      []string `json:"tools"` // agent-side names the profile offers, the code-mode tool included when codeMode
}

// ToolhiveSettings is the tools adapter's own section of the catalog (tools.toolhive).
type ToolhiveSettings struct {
	CodeModeTool string            `json:"codeModeTool"` // the name of the code-mode tool a profile with codeMode offers
	Profiles     []ToolhiveProfile `json:"profiles"`
}

// Profile returns the profile called name.
func (t *ToolhiveSettings) Profile(name string) (ToolhiveProfile, bool) {
	for _, p := range t.Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return ToolhiveProfile{}, false
}

// ToolSections holds one section per tools adapter.
type ToolSections struct {
	Toolhive *ToolhiveSettings `json:"toolhive"`
}

// Catalog is everything that exists on the platform. The top level is platform-level; what only one adapter needs is in
// that adapter's section under runtime or tools.
type Catalog struct {
	Adapters       Adapters          `json:"adapters"`
	Namespaces     Namespaces        `json:"namespaces"`
	GatewayBaseURL string            `json:"gatewayBaseUrl"`
	Models         []string          `json:"models"`
	Runtime        RuntimeSections   `json:"runtime"`
	Tools          ToolSections      `json:"tools"`
	Knowledge      Feature           `json:"knowledge"`
	Notify         Feature           `json:"notify"`
	Memory         Memory            `json:"memory"`
	Skills         map[string]string `json:"skills"`  // name -> OCI ref pinned by digest
	GateURL        string            `json:"gateUrl"` // empty when the A2A gate is off
	// ReservedNames are the names taken in the agents namespace (namespaces.agents) by chart agents and platform services.
	ReservedNames []string `json:"reservedNames"`
}

func (c *Catalog) HasModel(m string) bool { return slices.Contains(c.Models, m) }

// ToolProfile is a tool profile as the platform sees it: a name, what it offers, and an opaque reference to whatever
// serves it (only the runtime adapter reads Backend).
type ToolProfile struct {
	Name    string
	Backend string
	Tools   []string
}

// ToolResolver turns a profile name into what it offers. It is what the Tools adapter provides.
type ToolResolver interface {
	Profile(name string) (ToolProfile, bool)
	Profiles() []string
	// Missing explains, in one sentence, why the profile does not offer the tool.
	Missing(p ToolProfile, tool string) string
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
	if c.Adapters.Runtime == "kagent" {
		if k := c.Runtime.Kagent; k == nil {
			errs = append(errs, errors.New("runtime.kagent is required when adapters.runtime is kagent"))
		} else {
			need("runtime.kagent.harnessImage", k.HarnessImage)
			need("runtime.kagent.workerPool", k.WorkerPool)
			need("runtime.kagent.snapshotLocation", k.SnapshotLocation)
			if c.Memory.Available {
				need("runtime.kagent.memoryModelConfig", k.MemoryModelConfig)
			}
		}
	}
	if c.Adapters.Tools == "toolhive" {
		t := c.Tools.Toolhive
		if t == nil {
			errs = append(errs, errors.New("tools.toolhive is required when adapters.tools is toolhive"))
		} else {
			need("tools.toolhive.codeModeTool", t.CodeModeTool)
			seen := map[string]bool{}
			for i, p := range t.Profiles {
				if p.Name == "" || seen[p.Name] {
					errs = append(errs, fmt.Errorf("tools.toolhive.profiles[%d]: name is empty or used twice", i))
				}
				seen[p.Name] = true
				if p.ToolServer == "" {
					errs = append(errs, fmt.Errorf("tools.toolhive.profiles[%d] (%s): toolServer is required", i, p.Name))
				}
				if len(p.Tools) == 0 {
					errs = append(errs, fmt.Errorf("tools.toolhive.profiles[%d] (%s): tools must not be empty", i, p.Name))
				}
				if p.CodeMode != slices.Contains(p.Tools, t.CodeModeTool) {
					errs = append(errs, fmt.Errorf("tools.toolhive.profiles[%d] (%s): %s must be in tools exactly when codeMode is true", i, p.Name, t.CodeModeTool))
				}
			}
		}
	}
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
	for _, f := range []struct {
		n string
		f Feature
	}{{"knowledge", c.Knowledge}, {"notify", c.Notify}} {
		if f.f.Available && (len(f.f.Tools) == 0 || strings.TrimSpace(f.f.Prompt) == "") {
			errs = append(errs, fmt.Errorf("%s: available needs tools and prompt", f.n))
		}
	}
	if c.Memory.Available {
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
	// InClusterDestinations are the only in-cluster destinations (exact host:port) a definition's egress may name; the rule
	// that refuses every in-cluster destination has exactly this exception. Default none. Never the tools namespace.
	InClusterDestinations []string `json:"inClusterDestinations"`
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
	// ReservedNames are more names a definition may not use: "name" in every namespace, "namespace/name" in that one.
	ReservedNames []string `json:"reservedNames"`
	// SuspendAll is the operator's stop switch: every definition is treated as suspended.
	SuspendAll bool `json:"suspendAll"`
}

func allowed(list []string, v string) bool {
	return slices.Contains(list, "*") || slices.Contains(list, v)
}

func (p *Policy) ModelAllowed(m string) bool   { return allowed(p.AllowedModels, m) }
func (p *Policy) ProfileAllowed(n string) bool { return allowed(p.AllowedProfiles, n) }
func (p *Policy) SkillAllowed(n string) bool   { return allowed(p.AllowedSkills, n) }

// InClusterAllowed reports whether hostPort ("host:port", lower case) is one of the operator's listed destinations.
func (p *Policy) InClusterAllowed(hostPort string) bool {
	return slices.Contains(p.Egress.InClusterDestinations, hostPort)
}

// inClusterRE is a destination the operator may list: a service name qualified by its namespace (".svc" and
// ".svc.cluster.local" forms, or just <service>.<namespace>), a numeric port. No scheme, no wildcard, no address.
var inClusterRE = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)+:([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`)

var reservedRE = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?/)?[a-z]([-a-z0-9]*[a-z0-9])?$`)

// ReservedIn reports whether name is reserved in namespace ns. The catalog's names are those of chart agents and platform
// services, which live in the agents namespace; the policy's apply in every namespace ("name") or in one ("namespace/name").
func ReservedIn(c *Catalog, p *Policy, ns, name string) bool {
	if ns == c.Namespaces.Agents && slices.Contains(c.ReservedNames, name) {
		return true
	}
	return slices.Contains(p.ReservedNames, name) || slices.Contains(p.ReservedNames, ns+"/"+name)
}

// CheckPolicy checks the policy against the catalog: an in-cluster destination may never be inside the tools namespace
// (that would reach a tool server around its profile).
func CheckPolicy(c *Catalog, p *Policy) error {
	var errs []error
	for _, d := range p.Egress.InClusterDestinations {
		h, _, _ := strings.Cut(d, ":")
		ns := c.Namespaces.Tools
		for _, suf := range []string{ns, ns + ".svc", ns + ".svc.cluster.local"} {
			if h == suf || strings.HasSuffix(h, "."+suf) {
				errs = append(errs, fmt.Errorf("egress.inClusterDestinations entry %q is inside the tools namespace %q and can never be listed", d, ns))
				break
			}
		}
	}
	return errors.Join(errs...)
}

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
	seenDest := map[string]bool{}
	for _, d := range p.Egress.InClusterDestinations {
		if !inClusterRE.MatchString(d) || net.ParseIP(strings.Split(d, ":")[0]) != nil {
			errs = append(errs, fmt.Errorf("egress.inClusterDestinations entry %q must be host:port, with a namespace-qualified service name (service.namespace.svc:5000) and a numeric port", d))
		}
		if seenDest[d] {
			errs = append(errs, fmt.Errorf("egress.inClusterDestinations lists %q twice", d))
		}
		seenDest[d] = true
	}
	for _, n := range p.ReservedNames {
		if !reservedRE.MatchString(n) {
			errs = append(errs, fmt.Errorf("reservedNames entry %q must be a name or namespace/name", n))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	return &p, nil
}
