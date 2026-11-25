package api

// Reason is one documented value of a condition's reason. The table below is the whole vocabulary: a test of the core
// fails when the code sets a reason that is not here, and the README prints this table.
type Reason struct {
	Reason  string
	Status  string // the condition status it goes with
	Meaning string
}

// Reasons by condition type. Ready=True means: callable at status.endpoint, at status.revision. The other conditions
// are informational.
var reasons = map[string][]Reason{
	CondAccepted: {
		{"Accepted", "True", "the definition passes the catalog and the policy"},
		{"PolicyViolation", "False", "one or more rules refused it; each is one sentence in status.violations"},
	},
	CondKeyReady: {
		{"KeyReady", "True", "the agent's gateway key exists and its hash is registered"},
		{"KeyFailed", "False", "the key could not be created or registered; the message says why"},
		{"NotAccepted", "Unknown", "the definition is refused, so no key is made"},
		{"NotApplied", "Unknown", "an earlier step (the name check) stopped the pass before the key"},
	},
	CondApplied: {
		{"Applied", "True", "the runtime objects of this generation were written"},
		{"PreviousGenerationRunning", "True", "this generation is refused; the previously applied one keeps running"},
		{"NotAccepted", "False", "the definition is refused and nothing is running"},
		{"NotApplied", "False", "a step before the write failed or is waiting (the key)"},
		{"NameConflict", "False", "an object with a target name exists and is not owned by this definition; nothing was written"},
		{"CheckFailed", "False", "the name check could not read the cluster"},
		{"RenderFailed", "False", "the runtime adapter could not turn the plan into objects"},
		{"ApplyFailed", "False", "writing an object failed"},
		{"PruneFailed", "False", "removing an object that is no longer wanted failed"},
		{"RemoveFailed", "False", "removing the objects or the key of a definition that is no longer allowed failed"},
		{"PolicyRemoved", "False", "the catalog or the policy changed and what ran is no longer allowed: objects and key were removed"},
	},
	CondReady: {
		{"Ready", "True", "the agent is callable at status.endpoint at status.revision"},
		{"Suspended", "False", "suspended by spec.suspend or by the operator (policy suspendAll): the agent takes no new work; nothing is wrong"},
		{"NotAccepted", "False", "the definition is refused (or its objects were removed)"},
		{"NotApplied", "False", "the objects of this generation are not written yet or could not be"},
		{"ReadinessUnknown", "False", "the runtime's state could not be read"},
		{"NoEndpoint", "False", "the runtime reports the agent ready but the platform advertises no endpoint for it (the A2A gate is off), so it is not callable at status.endpoint"},
		{"RuntimeNotCreated", "False", "the runtime has not created the agent yet"},
		{"RuntimeStale", "False", "the runtime has not caught up with the latest spec yet"},
		{"RuntimePending", "False", "the runtime reports no verdict yet"},
		{"RuntimeNotReady", "False", "the runtime reports the agent not ready; its own words are in the message"},
		{"RuntimeRejected", "False", "the runtime refused the agent; its own words are in the message"},
	},
	CondSynced: {
		{"Synced", "True", "the last pass could read everything it needed"},
		{"TransientError", "False", "a read failed; the other conditions are the last known state, not a new verdict"},
	},
}

// ReasonsFor returns the documented reasons of a condition type, in table order.
func ReasonsFor(condType string) []Reason { return append([]Reason(nil), reasons[condType]...) }

// ValidReason reports whether reason is documented for the condition type.
func ValidReason(condType, reason string) bool {
	for _, r := range reasons[condType] {
		if r.Reason == reason {
			return true
		}
	}
	return false
}
