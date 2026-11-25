// Package policy holds the pure checks that decide whether an AgentDefinition is accepted. It reads the definition, the
// catalog (what exists) and the policy (what is allowed) and returns one sentence per refused rule. It writes nothing and
// calls nothing. The wording follows the chart's own failures where the chart has an equivalent.
package policy

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
)

const (
	MaxNameLen         = 40
	MaxDescriptionLen  = 256
	MaxInstructionsLen = 16 << 10
	MaxEgressOrigins   = 64
)

var nameRE = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// secretShapes are the ten patterns of charts/openagentix/files/memory-expiry.sh, with the same names, and one more of
// the platform's own (platform_virtual_key). PostgreSQL's word boundaries (\m, \M) became \b.
var secretShapes = []struct {
	name string
	re   *regexp.Regexp
}{
	{"aws_access_key_id", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"bearer_token", regexp.MustCompile(`[Bb]earer[ ]+[A-Za-z0-9._~+/=-]{24,}`)},
	{"github_token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,})`)},
	{"google_api_key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{"private_key_header", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"provider_secret_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{32,}`)},
	{"slack_token", regexp.MustCompile(`\bxox[abeprs]-[A-Za-z0-9-]{10,}`)},
	{"stripe_secret_key", regexp.MustCompile(`\b(sk|rk)_(live|test)_[0-9A-Za-z]{16,}`)},
	{"telegram_bot_token", regexp.MustCompile(`\b[0-9]{8,10}:[A-Za-z0-9_-]{35}`)},
	// not in the memory scan: the platform's own virtual keys (keys.Mint), which must never be pasted into a definition
	{"platform_virtual_key", regexp.MustCompile(`\bvk-[0-9a-f]{32}\b`)},
}

// PatternNames lists the secret shapes by name.
func PatternNames() []string {
	var n []string
	for _, s := range secretShapes {
		n = append(n, s.name)
	}
	return n
}

// Input is everything a check needs.
type Input struct {
	Def     *api.AgentDefinition
	Catalog *catalog.Catalog
	Policy  *catalog.Policy
	Tools   catalog.ToolResolver
	// Earlier holds the names of the other definitions that rank before this one (oldest first). They count against
	// maxAgents, so that when the limit is lowered the newest definitions are the ones refused.
	Earlier []string
}

// Validate returns the violations, in a fixed order; empty means accepted.
func Validate(in Input) []string {
	var v []string
	add := func(f string, a ...any) { v = append(v, fmt.Sprintf(f, a...)) }
	d, c, p := in.Def, in.Catalog, in.Policy
	s := d.Spec

	// name and count
	switch {
	case len(d.Metadata.Name) > MaxNameLen:
		add("name %q is too long: at most %d characters", d.Metadata.Name, MaxNameLen)
	case !nameRE.MatchString(d.Metadata.Name):
		add("name %q must be a DNS label (lower case letters, digits and dashes, starting with a letter)", d.Metadata.Name)
	}
	if slices.Contains(c.ReservedNames, d.Metadata.Name) || slices.Contains(p.ReservedNames, d.Metadata.Name) {
		add("name %q is reserved (chart agents and platform services own it)", d.Metadata.Name)
	}
	if len(in.Earlier) >= p.MaxAgents {
		add("the platform policy allows at most %d agents and %d already exist", p.MaxAgents, len(in.Earlier))
	}

	// size and content
	if n := len([]rune(s.Description)); n > MaxDescriptionLen {
		add("description is %d characters; at most %d", n, MaxDescriptionLen)
	}
	switch {
	case strings.TrimSpace(s.Instructions) == "":
		add("instructions are required")
	case len(s.Instructions) > MaxInstructionsLen:
		add("instructions are %d bytes; at most %d", len(s.Instructions), MaxInstructionsLen)
	}
	for _, sh := range secretShapes {
		if sh.re.MatchString(s.Instructions) {
			add("instructions contain something shaped like a secret (%s); remove it", sh.name)
		}
	}
	// every other free-text field: a secret in a description or a name would land in an object, a log or a status
	for _, f := range []struct {
		name string
		text []string
	}{
		{"description", []string{s.Description}}, {"model", []string{s.Model}}, {"toolProfile", []string{s.ToolProfile}},
		{"tools", s.Tools}, {"skills", s.Skills}, {"egress.allow", s.Egress.Allow},
	} {
		for _, sh := range secretShapes {
			if slices.ContainsFunc(f.text, sh.re.MatchString) {
				add("%s contains something shaped like a secret (%s); remove it", f.name, sh.name)
			}
		}
	}

	// model
	switch {
	case !c.HasModel(s.Model):
		add("model %q is not in the platform catalog (%s)", s.Model, strings.Join(c.Models, ", "))
	case !p.ModelAllowed(s.Model):
		add("model %q is not allowed by the platform policy", s.Model)
	}

	// tool profile and tools
	var prof catalog.Profile
	haveProf := false
	if s.ToolProfile != "" {
		prof, haveProf = in.Tools.Profile(s.ToolProfile)
		switch {
		case !haveProf:
			add("toolProfile %q is not defined (defined: %s)", s.ToolProfile, strings.Join(in.Tools.Profiles(), ", "))
		case !p.ProfileAllowed(s.ToolProfile):
			add("toolProfile %q is not allowed by the platform policy", s.ToolProfile)
		}
	}
	needProfile := func(what string) {
		if s.ToolProfile == "" {
			add("%s adds tools, so it needs a toolProfile: the shared tool server filters on the client side only", what)
		}
	}
	if s.ToolProfile == "" {
		switch {
		case len(s.Tools) > 0:
			add("the definition lists tools but names no toolProfile: the shared tool server filters on the client side only")
		case p.RequireToolProfile:
			add("the platform policy requires a toolProfile")
		}
	}
	var offered []string
	if haveProf {
		offered = prof.Tools
	}
	seenTool := map[string]bool{}
	checkTool := func(n string) {
		if !haveProf {
			return
		}
		if slices.Contains(offered, n) {
			return
		}
		if n == "execute_tool_script" {
			add("the definition lists tool \"execute_tool_script\" but profile %q has codeMode off (set codeMode: true on the profile)", prof.Name)
			return
		}
		add("the definition lists tool %q which is not in profile %q (the profile offers: %s)", n, prof.Name, strings.Join(offered, ", "))
	}
	for _, t := range s.Tools {
		if seenTool[t] {
			add("the definition lists tool %q twice", t)
			continue
		}
		seenTool[t] = true
		checkTool(t)
	}

	// knowledge and notify
	feature := func(name string, on bool, allow bool, f catalog.Feature) {
		if !on {
			return
		}
		switch {
		case !allow:
			add("%s is not allowed by the platform policy", name)
			return
		case !f.Available:
			add("%s is not available on this platform", name)
			return
		}
		needProfile(name)
		for _, t := range f.Tools {
			if !seenTool[t] {
				checkTool(t)
			}
		}
	}
	feature("knowledge", s.Knowledge, p.AllowKnowledge, c.Knowledge)
	feature("notify", s.Notify, p.AllowNotify, c.Notify)

	// memory
	if s.Memory.Enabled {
		switch {
		case !p.Memory.Allowed:
			add("memory is not allowed by the platform policy")
		case !c.Memory.Available:
			add("memory is not installed on this platform")
		default:
			if p.Memory.RequireExpiry && !c.Memory.ExpiryEnabled {
				add("the memory expiry job is off, so the time limit would not be enforced; memory is refused until an operator turns it on")
			}
			ttl := s.Memory.TTLDays
			if ttl < 0 {
				add("memory.ttlDays must be at least 1")
			} else {
				if ttl == 0 {
					ttl = c.Memory.DefaultTTLDays
				}
				if ttl > p.Memory.MaxTTLDays {
					add("memory.ttlDays %d is above the platform maximum of %d", ttl, p.Memory.MaxTTLDays)
				}
			}
		}
	}

	// skills
	seenSkill := map[string]bool{}
	for _, n := range s.Skills {
		if seenSkill[n] {
			add("skill %q is listed twice", n)
			continue
		}
		seenSkill[n] = true
		if _, ok := c.Skills[n]; !ok {
			add("skill %q is not in the platform catalog", n)
		} else if !p.SkillAllowed(n) {
			add("skill %q is not allowed by the platform policy", n)
		}
	}

	// egress
	if n := len(s.Egress.Allow); n > MaxEgressOrigins {
		add("egress.allow has %d origins; at most %d are allowed", n, MaxEgressOrigins)
	} else {
		seen := map[string]bool{}
		for _, o := range s.Egress.Allow {
			switch {
			case !catalog.OriginRE.MatchString(o) || len(o) > catalog.MaxOriginLen:
				add("egress.allow entry %q is not an origin: use https://host[:port] or http://host[:port], a lower case DNS name (a wildcard replaces exactly one leftmost label), no path, no query, no address", o)
			case seen[o]:
				add("egress.allow lists %q twice", o)
			case inNamespace(o, c.Namespaces.Tools):
				add("egress.allow entry %q is inside the tools namespace %q: that would reach a tool server around the profile", o, c.Namespaces.Tools)
			case internalHost(o, c.Namespaces.Agents, c.Namespaces.Gateway):
				add("egress.allow entry %q names a cluster-internal or local host (.svc, .cluster.local, a name without a dot, an address, or a platform namespace); the policy cannot allow it", o)
			case !originAllowed(o, p.Egress.AllowedOrigins):
				add("egress.allow entry %q is not matched by the platform policy", o)
			}
			seen[o] = true
		}
	}
	// a sentence that quotes a value (a model, a tool, an origin) must not repeat something shaped like a secret
	for i := range v {
		for _, sh := range secretShapes {
			v[i] = sh.re.ReplaceAllString(v[i], "[redacted]")
		}
	}
	return v
}

// internalHost reports whether the origin's host is something that is not on the public internet from the cluster's point
// of view, whatever the policy patterns say: an IP literal, a name without a dot, .svc and .cluster.local names, and names
// inside the given namespaces (short, .svc or full).
func internalHost(origin string, namespaces ...string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return true
	}
	h := strings.TrimPrefix(u.Hostname(), "*.")
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	if !strings.Contains(h, ".") || h == "svc" || strings.HasSuffix(h, ".svc") || strings.HasSuffix(h, ".cluster.local") || strings.Contains(h, ".svc.") {
		return true
	}
	for _, ns := range namespaces {
		if ns != "" && inNamespace(origin, ns) {
			return true
		}
	}
	return false
}

// inNamespace reports whether the origin's host is a name inside ns (short, .svc or full cluster name).
func inNamespace(origin, ns string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	h := strings.TrimPrefix(u.Hostname(), "*.")
	for _, suf := range []string{ns, ns + ".svc", ns + ".svc.cluster.local"} {
		if h == suf || strings.HasSuffix(h, "."+suf) {
			return true
		}
	}
	return false
}

// originAllowed matches an origin against the policy patterns. A pattern is an origin; its host may start with one
// wildcard label, which stands for exactly one label. An origin that is itself a wildcard matches only an identical pattern.
func originAllowed(origin string, patterns []string) bool {
	if slices.Contains(patterns, origin) {
		return true
	}
	o, err := url.Parse(origin)
	if err != nil || strings.HasPrefix(o.Hostname(), "*.") {
		return false
	}
	for _, pat := range patterns {
		p, err := url.Parse(pat)
		if err != nil || !strings.HasPrefix(p.Hostname(), "*.") {
			continue
		}
		if p.Scheme != o.Scheme || p.Port() != o.Port() {
			continue
		}
		suffix := p.Hostname()[1:] // ".example.org"
		host := o.Hostname()
		if strings.HasSuffix(host, suffix) && !strings.Contains(strings.TrimSuffix(host, suffix), ".") && len(host) > len(suffix) {
			return true
		}
	}
	return false
}
