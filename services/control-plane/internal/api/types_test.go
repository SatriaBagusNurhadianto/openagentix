package api

import (
	"encoding/json"
	"testing"
)

func TestDecodeDefinition(t *testing.T) {
	raw := `{"apiVersion":"openagentix.io/v1alpha1","kind":"AgentDefinition",
	"metadata":{"name":"researcher","namespace":"kagent","uid":"u1","generation":3,"resourceVersion":"9","finalizers":["x"],"creationTimestamp":"2026-11-25T10:00:00Z"},
	"spec":{"description":"d","model":"claude-haiku","instructions":"hi","toolProfile":"lookup","tools":["a"],"knowledge":true,"notify":false,
	"memory":{"enabled":true,"ttlDays":30},"skills":["s"],"egress":{"allow":["https://api.osv.dev"]}}}`
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	d, err := FromObject(m)
	if err != nil {
		t.Fatal(err)
	}
	if d.Metadata.Name != "researcher" || d.Metadata.Generation != 3 || d.Spec.Memory.TTLDays != 30 || !d.Spec.Knowledge || d.Spec.Egress.Allow[0] != "https://api.osv.dev" {
		t.Fatalf("decoded wrong: %+v", d)
	}
	if d.Deleting() {
		t.Fatal("not deleting")
	}
}

func TestDecodeRejectsWrongKind(t *testing.T) {
	if _, err := FromObject(map[string]any{"kind": "Agent", "apiVersion": "openagentix.io/v1alpha1"}); err == nil {
		t.Fatal("want error for wrong kind")
	}
}

func TestSetConditionKeepsTransitionTimeWhenStatusUnchanged(t *testing.T) {
	var s Status
	s.SetCondition(Condition{Type: "Ready", Status: "False", Reason: "A"}, "t1", 1)
	s.SetCondition(Condition{Type: "Ready", Status: "False", Reason: "B", Message: "m"}, "t2", 2)
	c := s.Condition("Ready")
	if c.LastTransitionTime != "t1" || c.Reason != "B" || c.ObservedGeneration != 2 {
		t.Fatalf("%+v", c)
	}
	s.SetCondition(Condition{Type: "Ready", Status: "True", Reason: "C"}, "t3", 2)
	if s.Condition("Ready").LastTransitionTime != "t3" {
		t.Fatal("transition time must move when status flips")
	}
}

func TestStatusPatchEmitsEmptyListsNotNull(t *testing.T) {
	p := (Status{}).Patch()
	st := p["status"].(map[string]any)
	for _, k := range []string{"conditions", "effectiveTools", "effectiveEgress", "violations"} {
		if v, ok := st[k].([]any); !ok || len(v) != 0 {
			t.Fatalf("%s should be an empty list, got %#v", k, st[k])
		}
	}
}
