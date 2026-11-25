// Package api holds the AgentDefinition types of openagentix.io/v1alpha1 exactly as ADR 0007 defines them. It has no
// upstream vocabulary: nothing here names kagent, agentgateway or ToolHive.
package api

import (
	"encoding/json"
	"fmt"
)

const (
	Group      = "openagentix.io"
	Version    = "v1alpha1"
	Kind       = "AgentDefinition"
	Plural     = "agentdefinitions"
	APIVersion = Group + "/" + Version

	// FinalizerGatewayKey keeps the definition until its key hash is removed from the gateway.
	FinalizerGatewayKey = "openagentix.io/gateway-key"

	LabelManagedBy  = "app.kubernetes.io/managed-by"
	ManagedByValue  = "openagentix-control-plane"
	LabelDefinition = "openagentix.io/agent-definition"

	CondAccepted = "Accepted"
	CondKeyReady = "KeyReady"
	CondApplied  = "Applied"
	CondReady    = "Ready"
	// CondSynced is present only after a pass could not read something it needed (the API server hiccuped). False with
	// reason TransientError means: the other conditions are the last known state, not a new verdict.
	CondSynced = "Synced"
)

// Condition types in the order status shows them.
var ConditionTypes = []string{CondAccepted, CondKeyReady, CondApplied, CondReady}

// ObjectMeta is the part of metadata the controller reads.
type ObjectMeta struct {
	Name              string   `json:"name"`
	Namespace         string   `json:"namespace,omitempty"`
	UID               string   `json:"uid,omitempty"`
	Generation        int64    `json:"generation,omitempty"`
	ResourceVersion   string   `json:"resourceVersion,omitempty"`
	CreationTimestamp string   `json:"creationTimestamp,omitempty"`
	DeletionTimestamp string   `json:"deletionTimestamp,omitempty"`
	Finalizers        []string `json:"finalizers,omitempty"`
}

type Memory struct {
	Enabled bool `json:"enabled"`
	TTLDays int  `json:"ttlDays,omitempty"`
}

type Egress struct {
	Allow []string `json:"allow,omitempty"`
}

// Spec is the whole builder surface. There is deliberately no field for a model config, image, tool server, egress
// policy reference, namespace, caller, gateway user or budget.
type Spec struct {
	Description  string   `json:"description,omitempty"`
	Model        string   `json:"model"`
	Instructions string   `json:"instructions"`
	ToolProfile  string   `json:"toolProfile,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Knowledge    bool     `json:"knowledge,omitempty"`
	Notify       bool     `json:"notify,omitempty"`
	Memory       Memory   `json:"memory,omitempty"`
	Skills       []string `json:"skills,omitempty"`
	Egress       Egress   `json:"egress,omitempty"`
}

type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	Message            string `json:"message,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// ObjectRef is the one generic reference to the upstream object, for people who debug.
type ObjectRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

type Status struct {
	ObservedGeneration int64       `json:"observedGeneration"`
	Conditions         []Condition `json:"conditions"`
	AgentRef           *ObjectRef  `json:"agentRef,omitempty"`
	GatewayUserID      string      `json:"gatewayUserId"`
	ToolServer         string      `json:"toolServer"`
	EffectiveTools     []string    `json:"effectiveTools"`
	EffectiveEgress    []string    `json:"effectiveEgress"`
	A2AURL             string      `json:"a2aUrl"`
	Violations         []string    `json:"violations"`

	// What the controller itself needs to remember between passes; none of it is secret.
	KeySecret         string `json:"keySecret"`         // name of the key Secret in use
	KeyFingerprint    string `json:"keyFingerprint"`    // head of the registered key's hash (keys.Fingerprint)
	AppliedGeneration int64  `json:"appliedGeneration"` // generation of the spec whose objects exist (0: none)
	AppliedSpec       string `json:"appliedSpec"`       // that spec as JSON; a string so a merge patch replaces it whole
}

type AgentDefinition struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       Spec       `json:"spec"`
	Status     Status     `json:"status"`
}

// FromObject decodes an unstructured object. Unknown fields are ignored here (the CRD schema prunes them); the
// controller only ever reads the fields above.
func FromObject(o map[string]any) (*AgentDefinition, error) {
	b, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	var d AgentDefinition
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("decode AgentDefinition: %w", err)
	}
	if d.Kind != Kind || d.APIVersion != APIVersion {
		return nil, fmt.Errorf("object is %s %s, not %s %s", d.APIVersion, d.Kind, APIVersion, Kind)
	}
	return &d, nil
}

// Deleting reports whether deletion was requested.
func (d *AgentDefinition) Deleting() bool { return d.Metadata.DeletionTimestamp != "" }

// HasFinalizer reports whether f is set.
func (d *AgentDefinition) HasFinalizer(f string) bool {
	for _, x := range d.Metadata.Finalizers {
		if x == f {
			return true
		}
	}
	return false
}

// Condition returns the condition of type t (zero value when absent).
func (s Status) Condition(t string) Condition {
	for _, c := range s.Conditions {
		if c.Type == t {
			return c
		}
	}
	return Condition{}
}

// SetCondition sets c. LastTransitionTime moves only when the status value changes; now is the time to use then.
func (s *Status) SetCondition(c Condition, now string, generation int64) {
	c.ObservedGeneration = generation
	for i, old := range s.Conditions {
		if old.Type != c.Type {
			continue
		}
		if old.Status == c.Status && old.LastTransitionTime != "" {
			c.LastTransitionTime = old.LastTransitionTime
		} else {
			c.LastTransitionTime = now
		}
		s.Conditions[i] = c
		return
	}
	c.LastTransitionTime = now
	s.Conditions = append(s.Conditions, c)
}

// Patch returns the merge patch body for the status subresource. Lists are always present (empty, never null), so
// that a merge patch clears old values.
func (s Status) Patch() map[string]any {
	if s.Conditions == nil {
		s.Conditions = []Condition{}
	}
	if s.EffectiveTools == nil {
		s.EffectiveTools = []string{}
	}
	if s.EffectiveEgress == nil {
		s.EffectiveEgress = []string{}
	}
	if s.Violations == nil {
		s.Violations = []string{}
	}
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if s.AgentRef == nil {
		m["agentRef"] = nil // merge patch: null removes the field
	}
	return map[string]any{"status": m}
}
