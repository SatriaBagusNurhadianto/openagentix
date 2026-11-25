package model

import "slices"

var errorReasons = []string{"ApplyFailed", "RenderFailed", "CheckFailed", "PruneFailed", "NameConflict"}

// StateOf maps the conditions of a definition to the badge word, following UI-SPEC section 7:
//
//	spec.suspend                                paused (after refused)
//	Ready=True                                  ready
//	Accepted=False                              refused
//	KeyReady=False, or Applied=False with one
//	of ApplyFailed, RenderFailed, CheckFailed,
//	PruneFailed, NameConflict                   error
//	anything else                               pending
//
// A definition that is being deleted is pending.
func StateOf(conds []Condition, deleting, suspended bool) (State, string) {
	get := func(t string) (Condition, bool) {
		for _, c := range conds {
			if c.Type == t {
				return c, true
			}
		}
		return Condition{}, false
	}
	say := func(c Condition) string {
		switch {
		case c.Reason != "" && c.Message != "":
			return c.Reason + ": " + c.Message
		case c.Reason != "":
			return c.Reason
		}
		return c.Message
	}
	if deleting {
		return StatePending, "Deleting: the controller is removing what it made for this definition"
	}
	if a, ok := get("Accepted"); ok && a.Status == "False" {
		return StateRefused, say(a)
	}
	if suspended {
		return StatePaused, "Paused: spec.suspend is true, so the agent stays and refuses new work"
	}
	if r, ok := get("Ready"); ok && r.Status == "True" {
		return StateReady, ""
	}
	if k, ok := get("KeyReady"); ok && k.Status == "False" {
		return StateError, say(k)
	}
	if a, ok := get("Applied"); ok && a.Status == "False" && slices.Contains(errorReasons, a.Reason) {
		return StateError, say(a)
	}
	if r, ok := get("Ready"); ok {
		return StatePending, say(r)
	}
	if len(conds) == 0 {
		return StatePending, "The controller has not looked at this definition yet"
	}
	for _, c := range conds {
		if c.Status != "True" {
			return StatePending, say(c)
		}
	}
	return StatePending, ""
}
