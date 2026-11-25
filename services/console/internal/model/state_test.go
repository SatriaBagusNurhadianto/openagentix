package model

import "testing"

func c(t, s, r string) Condition {
	return Condition{Type: t, Status: s, Reason: r, Message: r + " message"}
}

func TestStateOf(t *testing.T) {
	ok := []Condition{c("Accepted", "True", "Accepted"), c("KeyReady", "True", "KeyReady"), c("Applied", "True", "Applied"), c("Ready", "True", "Ready")}
	for name, tc := range map[string]struct {
		conds    []Condition
		deleting bool
		want     State
	}{
		"ready":            {ok, false, StateReady},
		"deleting":         {ok, true, StatePending},
		"none yet":         {nil, false, StatePending},
		"refused":          {[]Condition{c("Accepted", "False", "PolicyViolation"), c("Ready", "False", "NotAccepted")}, false, StateRefused},
		"key failed":       {[]Condition{c("Accepted", "True", "Accepted"), c("KeyReady", "False", "KeyFailed")}, false, StateError},
		"apply failed":     {[]Condition{c("Accepted", "True", "Accepted"), c("Applied", "False", "ApplyFailed"), c("Ready", "False", "NotApplied")}, false, StateError},
		"name conflict":    {[]Condition{c("Accepted", "True", "Accepted"), c("Applied", "False", "NameConflict")}, false, StateError},
		"waiting runtime":  {[]Condition{c("Accepted", "True", "A"), c("KeyReady", "True", "K"), c("Applied", "True", "A"), c("Ready", "False", "AgentPending")}, false, StatePending},
		"unknown":          {[]Condition{c("Accepted", "True", "A"), c("KeyReady", "Unknown", "Pending")}, false, StatePending},
		"not accepted yet": {[]Condition{c("Accepted", "False", "PolicyViolation"), c("Applied", "False", "NotAccepted")}, false, StateRefused},
	} {
		if got, _ := StateOf(tc.conds, tc.deleting, false); got != tc.want {
			t.Errorf("%s: got %s want %s", name, got, tc.want)
		}
	}
}

func TestPausedComesAfterRefused(t *testing.T) {
	ok := []Condition{c("Accepted", "True", "Accepted"), c("Ready", "True", "Ready")}
	if got, _ := StateOf(ok, false, true); got != StatePaused {
		t.Fatal(got)
	}
	if got, _ := StateOf([]Condition{c("Accepted", "False", "PolicyViolation")}, false, true); got != StateRefused {
		t.Fatal(got)
	}
}
