package core

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/keys"
	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
	"github.com/openagentix/openagentix/services/control-plane/internal/kubetest"
	"github.com/openagentix/openagentix/services/control-plane/internal/plan"
)

// uid8 is the first 8 hex characters of the UID without dashes: the suffix of the key Secret's name.
func uid8(raw kubetest.Object) string {
	return strings.ReplaceAll(kube.Meta(raw).String("uid"), "-", "")[:8]
}

func (e *env) raw(name string) kubetest.Object {
	o, ok := e.srv.Get("agentdefinitions", "kagent", name)
	if !ok {
		panic("no definition " + name)
	}
	return o
}

func (e *env) writes() []kubetest.Request {
	var out []kubetest.Request
	for _, r := range e.srv.Requests() {
		if r.Method == "POST" || r.Method == "DELETE" || (r.Method == "PATCH" && strings.Contains(r.Path, "example.dev")) {
			out = append(out, r)
		}
	}
	return out
}

// ---- I1: the key Secret's name carries the definition's UID ----

func TestForeignSecretWithTheOldNameIsNeverDeleted(t *testing.T) {
	e, _ := newEnv(t)
	// someone else owns a Secret called <name>-virtual-key: unlabelled, no owner reference
	e.srv.Seed("secrets", kubetest.Object{"apiVersion": "v1", "kind": "Secret", "metadata": kubetest.Object{"name": "researcher-virtual-key", "namespace": "kagent"},
		"data": kubetest.Object{"OPENAI_API_KEY": "Zm9yZWlnbg=="}})
	e.seed("researcher", nil)
	e.pass(t)
	if _, ok := e.srv.Get("secrets", "kagent", "researcher-virtual-key"); !ok {
		t.Fatal("a Secret the controller does not own was deleted")
	}
	for _, r := range e.srv.Matching("DELETE", "/secrets/") {
		t.Fatalf("a Secret was deleted: %s", r.Path)
	}
	want := "researcher-virtual-key-" + uid8(e.raw("researcher"))
	if _, ok := e.srv.Get("secrets", "kagent", want); !ok {
		t.Fatalf("the key Secret must be named %s", want)
	}
	wantCond(t, e.status("researcher"), "Ready", "True", "Ready")
	// the model config points at that name
	w, _ := e.srv.Get("widgets", "kagent", "researcher-w")
	if got := w["spec"].(map[string]any)["key"]; got != want {
		t.Fatalf("the objects reference %v, want %s", got, want)
	}
}

// ---- I3: ownership is labels AND an owner reference with the definition's UID ----

func TestLabelledObjectWithoutOwnerReferenceIsNotOurs(t *testing.T) {
	for name, refs := range map[string][]any{
		"no owner reference": nil,
		"another uid": {kubetest.Object{"apiVersion": "openagentix.io/v1alpha1", "kind": "AgentDefinition", "name": "researcher",
			"uid": "00000000-1111-4222-8333-444444444444", "controller": true}},
		"not the controller": {kubetest.Object{"apiVersion": "openagentix.io/v1alpha1", "kind": "AgentDefinition", "name": "researcher", "uid": "SAME"}},
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := newEnv(t)
			def := e.srv.Seed("agentdefinitions", defObj("researcher", nil))
			for _, r := range refs {
				if r.(kubetest.Object)["uid"] == "SAME" {
					r.(kubetest.Object)["uid"] = kube.Meta(def).String("uid")
				}
			}
			m := kubetest.Object{"name": "researcher-g", "namespace": "kagent", "labels": kubetest.Object{
				"app.kubernetes.io/managed-by": "openagentix-control-plane", "openagentix.io/agent-definition": "researcher"}}
			if refs != nil {
				m["ownerReferences"] = refs
			}
			e.srv.Seed("gadgets", kubetest.Object{"apiVersion": "example.dev/v1", "kind": "Gadget", "metadata": m, "spec": kubetest.Object{"mine": false}})
			e.pass(t)
			wantCond(t, e.status("researcher"), "Applied", "False", "NameConflict")
			if len(e.writes()) != 0 {
				t.Fatalf("name conflict, yet writes: %+v", e.writes())
			}
		})
	}
}

// ---- I4: a kind that is not installed means nothing to prune ----

func (e *env) missingKinds(plurals ...string) {
	e.srv.Intercept = func(r kubetest.Request) (int, any, bool) {
		if r.Method != "GET" {
			return 0, nil, false
		}
		for _, p := range plurals {
			if strings.HasSuffix(r.Path, "/"+p) {
				return 404, map[string]any{"kind": "Status", "reason": "NotFound"}, true
			}
		}
		return 0, nil, false
	}
}

func TestMissingKindOnListIsNothingToPrune(t *testing.T) {
	e, _ := newEnv(t)
	e.missingKinds("gadgets")
	e.seed("researcher", nil)
	e.pass(t)
	st := e.status("researcher")
	wantCond(t, st, "Applied", "True", "Applied")
	wantCond(t, st, "Ready", "True", "Ready")
}

func TestDeletionCompletesWhenAKindIsMissing(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	e.missingKinds("gadgets")
	cl := kube.New(e.srv.URL, e.srv.Client(), func() (string, error) { return "t", nil })
	if err := cl.Delete(context.Background(), definitions, "kagent", "researcher"); err != nil {
		t.Fatal(err)
	}
	e.pass(t)
	if _, ok := e.srv.Get("agentdefinitions", "kagent", "researcher"); ok {
		t.Fatal("the definition is wedged: a missing kind must not hold the finalizer")
	}
	if _, ok := e.gw.entries["kagent.researcher"]; ok {
		t.Fatal("hash must be unregistered")
	}
}

func TestFinalizerRemovalToleratesAnObjectWithoutFinalizers(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	cl := kube.New(e.srv.URL, e.srv.Client(), func() (string, error) { return "t", nil })
	_ = cl.Delete(context.Background(), definitions, "kagent", "researcher")
	e.srv.Intercept = func(r kubetest.Request) (int, any, bool) { // the freshly read object has no finalizers field
		if r.Method == "GET" && strings.HasSuffix(r.Path, "/agentdefinitions/researcher") {
			return 200, map[string]any{"apiVersion": "openagentix.io/v1alpha1", "kind": "AgentDefinition", "metadata": map[string]any{"name": "researcher", "namespace": "kagent"}}, true
		}
		return 0, nil, false
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	_ = e.c.ReconcileAll(context.Background())
}

// ---- I5: gateway entries without a live definition are removed ----

func TestOrphanedGatewayEntriesAreSwept(t *testing.T) {
	e, _ := newEnv(t)
	e.gw.entries["ghost"] = "sha256:dead"
	e.seed("researcher", nil)
	e.pass(t)
	if _, ok := e.gw.entries["ghost"]; ok {
		t.Fatal("the entry of a definition that does not exist must go")
	}
	if _, ok := e.gw.entries["kagent.researcher"]; !ok {
		t.Fatal("a definition being created in the same pass must not be swept")
	}
}

func TestSweepKeepsEntriesOfDeletingAndRefusedDefinitions(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("good", nil)
	e.seed("bad", func(s map[string]any) { s["model"] = "gpt-9" })
	e.pass(t)
	e.gw.entries["kagent.bad"] = "sha256:left-from-before"
	e.pass(t)
	if _, ok := e.gw.entries["kagent.bad"]; !ok {
		t.Fatal("a listed definition's entry is not an orphan")
	}
	_ = api.Kind
}

// ---- helpers for the loop tests ----

func (g *fakeGateway) registerCalls() int { g.mu.Lock(); defer g.mu.Unlock(); return g.regCalls }

func (e *env) bumpGeneration(name string, mut func(spec map[string]any)) {
	e.srv.Modify("agentdefinitions", "kagent", name, func(o kubetest.Object) {
		m := kube.Meta(o)
		g, _ := m["generation"].(float64)
		m["generation"] = g + 1
		if mut != nil {
			mut(o["spec"].(map[string]any))
		}
	})
}

func (e *env) rawStatus(name string) map[string]any {
	st, _ := e.raw(name)["status"].(map[string]any)
	return st
}

// ctl is status.controller, the controller's own bookkeeping.
func (e *env) ctl(name string) map[string]any {
	c, _ := e.rawStatus(name)["controller"].(map[string]any)
	return c
}

func (e *env) runLoop(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func eventually(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- I2: a refused definition and the objects that run ----

func TestEditedIntoRefusedKeepsThePreviousAgentWhenItStillPasses(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	before := e.status("researcher")
	e.bumpGeneration("researcher", func(s map[string]any) { s["model"] = "gpt-9" }) // the author's edit is refused
	e.pass(t)

	for _, n := range [][2]string{{"widgets", "researcher-w"}, {"gadgets", "researcher-g"}} {
		if _, ok := e.srv.Get(n[0], "kagent", n[1]); !ok {
			t.Fatalf("%s/%s: the previous version must keep running", n[0], n[1])
		}
	}
	st := e.status("researcher")
	wantCond(t, st, "Accepted", "False", "PolicyViolation")
	wantCond(t, st, "Applied", "True", "PreviousGenerationRunning")
	wantCond(t, st, "Ready", "True", "Ready")
	if st.ObservedGeneration != 2 || cond(st, "Accepted").ObservedGeneration != 2 {
		t.Fatalf("the refused generation was observed: %+v", st)
	}
	if cond(st, "Applied").ObservedGeneration != 1 || cond(st, "Ready").ObservedGeneration != 1 {
		t.Fatalf("Applied and Ready describe generation 1: %+v %+v", cond(st, "Applied"), cond(st, "Ready"))
	}
	if endpointURL(st) != endpointURL(before) || endpointURL(st) == "" || len(st.EffectiveTools) != 1 || st.AgentRef == nil || len(st.Violations) == 0 {
		t.Fatalf("status must still describe what runs: %+v", st)
	}
	if got := e.ctl("researcher")["appliedGeneration"]; got != float64(1) {
		t.Fatalf("appliedGeneration %v", got)
	}
	if len(e.srv.Matching("DELETE", "example.dev")) != 0 {
		t.Fatal("nothing may be deleted")
	}
}

func TestPreviousAgentReadinessIsReadFromTheRuntimeWhileARefusalStands(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	e.bumpGeneration("researcher", func(s map[string]any) { s["model"] = "gpt-9" })
	e.rt.ready = plan.Readiness{Reason: "RuntimeNotReady", Message: "pod restarting"}
	e.pass(t)
	st := e.status("researcher")
	wantCond(t, st, "Applied", "True", "PreviousGenerationRunning")
	wantCond(t, st, "Ready", "False", "RuntimeNotReady")
}

func TestPolicyTighteningRemovesTheRunningAgentAndSaysSo(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	e.pol.AllowedModels = []string{"some-other-model"} // the operator tightened the policy; the definition is unchanged
	e.pass(t)

	for _, n := range [][2]string{{"widgets", "researcher-w"}, {"gadgets", "researcher-g"}} {
		if _, ok := e.srv.Get(n[0], "kagent", n[1]); ok {
			t.Fatalf("%s/%s must be removed: it no longer passes the policy", n[0], n[1])
		}
	}
	if _, ok := e.srv.Get("agentdefinitions", "kagent", "researcher"); !ok {
		t.Fatal("the definition stays")
	}
	if _, ok := e.gw.entries["kagent.researcher"]; ok {
		t.Fatal("the key hash goes with the agent")
	}
	st := e.status("researcher")
	wantCond(t, st, "Accepted", "False", "PolicyViolation")
	wantCond(t, st, "Applied", "False", "PolicyRemoved")
	wantCond(t, st, "Ready", "False", "NotAccepted")
	if endpointURL(st) != "" || st.AgentRef != nil || len(st.EffectiveTools) != 0 || len(st.EffectiveEgress) != 0 || st.Controller.ToolServer != "" {
		t.Fatalf("status must not describe an agent that is not running: %+v", st)
	}
	if got := e.ctl("researcher")["appliedGeneration"]; got != float64(0) {
		t.Fatalf("appliedGeneration %v", got)
	}
	// the operator loosens the policy again: the agent comes back with a new key
	e.pol.AllowedModels = []string{"claude-haiku"}
	e.mint = "vk-ffffffffffffffffffffffffffffffff"
	e.pass(t)
	wantCond(t, e.status("researcher"), "Ready", "True", "Ready")
	if e.gw.entries["kagent.researcher"] != keys.Hash(e.mint) {
		t.Fatal("the key must be replaced after the removal")
	}
}

func TestEditedAndTightenedAtOnceRemovesTheAgent(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	e.pol.AllowedModels = []string{"some-other-model"}
	e.bumpGeneration("researcher", func(s map[string]any) { s["instructions"] = "Different." })
	e.pass(t)
	if _, ok := e.srv.Get("widgets", "kagent", "researcher-w"); ok {
		t.Fatal("the previously applied spec no longer passes: it must not keep running")
	}
	wantCond(t, e.status("researcher"), "Applied", "False", "PolicyRemoved")
}

func TestRefusalWithUnknownLastAppliedRemovesTheAgent(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	// a status written by an older controller: running, but no record of what was applied
	e.srv.Modify("agentdefinitions", "kagent", "researcher", func(o kubetest.Object) {
		st := o["status"].(map[string]any)["controller"].(map[string]any)
		delete(st, "appliedSpec")
		delete(st, "appliedGeneration")
	})
	e.bumpGeneration("researcher", func(s map[string]any) { s["model"] = "gpt-9" })
	e.pass(t)
	if _, ok := e.srv.Get("widgets", "kagent", "researcher-w"); ok {
		t.Fatal("unknown last-applied: the running objects cannot be vouched for and must go")
	}
}

func TestRefusedBeforeEverRunningTouchesNothing(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("bad", func(s map[string]any) { s["model"] = "gpt-9" })
	e.pass(t)
	e.pass(t)
	if len(e.srv.Matching("DELETE", "")) != 0 {
		t.Fatal("nothing ran, nothing to delete")
	}
	wantCond(t, e.status("bad"), "Applied", "False", "NotAccepted")
}

// ---- I6: pagination, throttling, one broken object ----

func TestAllPagesOfDefinitionsAreReconciled(t *testing.T) {
	old := kube.ListPageSize
	kube.ListPageSize = 2
	defer func() { kube.ListPageSize = old }()
	e, _ := newEnv(t)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		e.seed(n, nil)
	}
	e.pol.MaxAgents = 10
	e.pass(t)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		wantCond(t, e.status(n), "Ready", "True", "Ready")
	}
}

func TestPassesAreThrottledToTheMinimumInterval(t *testing.T) {
	e, _ := newEnv(t)
	e.c.cfg.MinInterval = 250 * time.Millisecond
	e.c.cfg.Resync = time.Hour
	e.seed("one", nil)
	e.runLoop(t)
	eventually(t, "ready", 5*time.Second, func() bool { return e.status("one").Condition("Ready").Status == "True" })
	start := time.Now()
	lists := len(e.srv.Matching("GET", "/agentdefinitions"))
	for i := 0; i < 40; i++ { // a storm of spec changes
		e.bumpGeneration("one", nil)
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(time.Until(start.Add(time.Second)))
	got := 0
	for _, r := range e.srv.Matching("GET", "/agentdefinitions")[lists:] {
		if !strings.Contains(r.Query, "watch=true") {
			got++
		}
	}
	if got > 5 {
		t.Fatalf("%d list requests in one second with a minimum interval of 250 ms", got)
	}
}

func TestOneMalformedObjectDoesNotStopTheOthers(t *testing.T) {
	e, _ := newEnv(t)
	bad := defObj("broken", nil)
	bad["spec"].(map[string]any)["tools"] = "not-a-list"
	e.srv.Seed("agentdefinitions", bad)
	e.seed("fine", nil)
	e.pass(t)
	wantCond(t, e.status("fine"), "Ready", "True", "Ready")
}

// ---- I7 / M1: the key's fingerprint ----

func TestKeyFingerprintIsRecordedAndReachesTheObjects(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	fp := keys.Fingerprint(keys.Hash(e.mint))
	st := e.ctl("researcher")
	if st["keyFingerprint"] != fp {
		t.Fatalf("status keyFingerprint %v, want %s", st["keyFingerprint"], fp)
	}
	secret := "researcher-virtual-key-" + uid8(e.raw("researcher"))
	if st["keySecret"] != secret {
		t.Fatalf("status keySecret %v", st["keySecret"])
	}
	w, _ := e.srv.Get("widgets", "kagent", "researcher-w")
	if w["spec"].(map[string]any)["fp"] != fp {
		t.Fatalf("the objects must carry the fingerprint: %v", w["spec"])
	}
	sec, _ := e.srv.Get("secrets", "kagent", secret)
	if a, _ := kube.Meta(sec)["annotations"].(map[string]any); a["openagentix.io/key-fingerprint"] != fp {
		t.Fatalf("secret annotations %v", a)
	}
}

func TestARestoredOlderHashIsDetectedAndTheKeyReplaced(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	oldFP := keys.Fingerprint(keys.Hash(e.mint))
	e.gw.entries["kagent.researcher"] = "sha256:0000000000000000deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef" // a backup restored
	e.mint = "vk-ffffffffffffffffffffffffffffffff"
	e.pass(t)
	if e.gw.entries["kagent.researcher"] != keys.Hash(e.mint) {
		t.Fatalf("the key must be replaced: %v", e.gw.entries)
	}
	newFP := keys.Fingerprint(keys.Hash(e.mint))
	if got := e.ctl("researcher")["keyFingerprint"]; got != newFP || newFP == oldFP {
		t.Fatalf("fingerprint %v", got)
	}
	w, _ := e.srv.Get("widgets", "kagent", "researcher-w")
	if w["spec"].(map[string]any)["fp"] != newFP {
		t.Fatal("the objects must roll with the new key")
	}
	wantCond(t, e.status("researcher"), "KeyReady", "True", "KeyReady")
}

func TestALostEntryIsRegisteredAgain(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	delete(e.gw.entries, "kagent.researcher")
	e.mint = "vk-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	e.pass(t)
	if e.gw.entries["kagent.researcher"] != keys.Hash(e.mint) {
		t.Fatal("not registered again")
	}
}

func TestSteadyStateMintsNothingAndSendsNoSecretRequests(t *testing.T) {
	e, _ := newEnv(t)
	mints := 0
	e.c.cfg.Mint = func() (string, error) { mints++; return e.mint, nil }
	e.seed("researcher", nil)
	for range 4 {
		e.pass(t)
	}
	if mints != 1 {
		t.Fatalf("%d keys minted over four passes, want 1", mints)
	}
	if got := len(e.srv.Matching("POST", "/secrets")); got != 1 {
		t.Fatalf("%d Secret POSTs, want 1", got)
	}
	if got := len(e.srv.Matching("DELETE", "/secrets")); got != 0 {
		t.Fatalf("%d Secret DELETEs", got)
	}
}

// ---- M2: the watch ----

func TestAHungWatchOpenDoesNotStopTheLoop(t *testing.T) {
	for name, tc := range map[string]struct{ resync, open time.Duration }{
		"open deadline":                     {time.Hour, 100 * time.Millisecond},
		"resync timer runs during the open": {150 * time.Millisecond, time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := newEnv(t)
			release := make(chan struct{})
			var hung atomic.Int32
			e.srv.Intercept = func(r kubetest.Request) (int, any, bool) {
				if r.Method == "GET" && strings.Contains(r.Query, "watch=true") && hung.Add(1) == 1 {
					<-release // the first watch never answers
				}
				return 0, nil, false
			}
			defer close(release)
			e.c.cfg.Resync, e.c.cfg.WatchOpenTimeout = tc.resync, tc.open
			e.seed("first", nil)
			e.runLoop(t)
			eventually(t, "first ready", 5*time.Second, func() bool { return e.status("first").Condition("Ready").Status == "True" })
			eventually(t, "the watch was opened", 5*time.Second, func() bool { return hung.Load() >= 1 })
			e.seed("second", nil)
			eventually(t, "second ready although the first watch hangs", 3*time.Second, func() bool {
				o, ok := e.srv.Get("agentdefinitions", "kagent", "second")
				if !ok {
					return false
				}
				d, _ := api.FromObject(o)
				return d.Status.Condition("Ready").Status == "True"
			})
		})
	}
}

// ---- M3: status writes ----

func TestOverLongReadinessTextIsCutToTheCRDLimitsOnARuneBoundary(t *testing.T) {
	e, _ := newEnv(t)
	e.rt.ready = plan.Readiness{Reason: strings.Repeat("€", 100), Message: strings.Repeat("€", 3000)}
	e.seed("researcher", nil)
	e.pass(t)
	c := cond(e.status("researcher"), "Ready")
	// a reason outside the documented table is mapped into it (its own word goes to the message), which is cut to the CRD limit
	if r, m := []rune(c.Reason), []rune(c.Message); c.Reason != "RuntimeNotReady" || len(r) > 64 || len(m) != 1024 || strings.ContainsRune(c.Reason+c.Message, '�') {
		t.Fatalf("reason %d runes, message %d runes", len(r), len(m))
	}
}

func TestAnInvalidStatusIsNotRetriedForever(t *testing.T) {
	e, _ := newEnv(t)
	e.srv.Intercept = func(r kubetest.Request) (int, any, bool) {
		if r.Method == "PATCH" && strings.HasSuffix(r.Path, "/status") {
			return 422, map[string]any{"kind": "Status", "reason": "Invalid"}, true
		}
		return 0, nil, false
	}
	e.c.cfg.Resync = time.Hour
	e.seed("researcher", nil)
	if err := e.c.ReconcileAll(context.Background()); err != nil {
		t.Fatalf("a 422 on the status is not something a retry fixes: %v", err)
	}
	e.runLoop(t)
	time.Sleep(300 * time.Millisecond)
	if n := len(e.srv.Matching("PATCH", "/agentdefinitions/researcher/status")); n > 3 {
		t.Fatalf("%d status patches: a 422 is retried forever", n)
	}
}

// ---- M4: backoff ----

func TestAFailingDefinitionBacksOffExponentially(t *testing.T) {
	e, _ := newEnv(t)
	e.c.cfg.Backoff = 20 * time.Millisecond
	e.c.cfg.Resync = time.Hour
	e.gw.failReg = errors.New("gateway down")
	e.seed("researcher", nil)
	e.runLoop(t)
	time.Sleep(500 * time.Millisecond)
	if n := e.gw.registerCalls(); n < 2 || n > 8 {
		t.Fatalf("%d attempts in 500 ms with a 20 ms base: want a few, spaced out (flat retry would make about 25)", n)
	}
}

func TestNameConflictIsTerminalUntilTheGenerationChanges(t *testing.T) {
	e, _ := newEnv(t)
	e.c.cfg.Backoff = 10 * time.Millisecond
	e.c.cfg.Resync = time.Hour
	e.srv.Seed("gadgets", kubetest.Object{"apiVersion": "example.dev/v1", "kind": "Gadget", "metadata": kubetest.Object{"name": "researcher-g", "namespace": "kagent"}})
	e.seed("researcher", nil)
	e.runLoop(t)
	eventually(t, "conflict reported", 5*time.Second, func() bool { return e.status("researcher").Condition("Applied").Reason == "NameConflict" })
	time.Sleep(100 * time.Millisecond)
	n := len(e.srv.Matching("GET", "/gadgets/researcher-g"))
	time.Sleep(300 * time.Millisecond)
	if m := len(e.srv.Matching("GET", "/gadgets/researcher-g")); m != n {
		t.Fatalf("the conflict is re-checked on its own: %d -> %d", n, m)
	}
	e.srv.Modify("gadgets", "kagent", "researcher-g", func(o kubetest.Object) {}) // not our object: remove it by hand
	cl := kube.New(e.srv.URL, e.srv.Client(), func() (string, error) { return "t", nil })
	_ = cl.Delete(context.Background(), kube.Resource{Group: "example.dev", Version: "v1", Plural: "gadgets", Namespaced: true}, "kagent", "researcher-g")
	e.bumpGeneration("researcher", func(s map[string]any) { s["description"] = "changed" })
	eventually(t, "recovered after the edit", 5*time.Second, func() bool { return e.status("researcher").Condition("Ready").Status == "True" })
}

// ---- M5: maxAgents counts accepted definitions and never displaces one ----

func TestMaxAgentsCountsAcceptedDefinitionsOnlyAndNeverDisplacesOne(t *testing.T) {
	e, _ := newEnv(t)
	e.pol.MaxAgents = 1
	older := defObj("older", func(s map[string]any) { s["model"] = "gpt-9" }) // refused for its own reasons
	older["metadata"].(map[string]any)["creationTimestamp"] = "2026-11-25T09:00:00Z"
	e.srv.Seed("agentdefinitions", older)
	newer := defObj("newer", nil)
	newer["metadata"].(map[string]any)["creationTimestamp"] = "2026-11-25T11:00:00Z"
	e.srv.Seed("agentdefinitions", newer)
	e.pass(t)
	wantCond(t, e.status("newer"), "Accepted", "True", "Accepted") // the refused older one holds no place
	wantCond(t, e.status("older"), "Accepted", "False", "PolicyViolation")

	// the author fixes the older one: it ranks first by age, but the agent that is already accepted keeps its place
	e.bumpGeneration("older", func(s map[string]any) { s["model"] = "claude-haiku" })
	e.pass(t)
	wantCond(t, e.status("newer"), "Ready", "True", "Ready")
	st := e.status("older")
	wantCond(t, st, "Accepted", "False", "PolicyViolation")
	if len(st.Violations) != 1 || !strings.Contains(st.Violations[0], "at most 1 agents") {
		t.Fatalf("%q", st.Violations)
	}
}

// ---- M9: a transient read error is not a verdict ----

func TestATransientReadErrorKeepsTheRunningStateAndSaysSoSeparately(t *testing.T) {
	for name, fail := range map[string]func(e *env){
		"conflict check": func(e *env) {
			e.srv.Intercept = func(r kubetest.Request) (int, any, bool) {
				if r.Method == "GET" && strings.HasSuffix(r.Path, "/gadgets/researcher-g") {
					return 500, map[string]any{"kind": "Status", "reason": "InternalError"}, true
				}
				return 0, nil, false
			}
		},
		"readiness": func(e *env) { e.rt.readyErr = errors.New("get agent: 500") },
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := newEnv(t)
			e.seed("researcher", nil)
			e.pass(t)
			fail(e)
			_ = e.c.ReconcileAll(context.Background())
			st := e.status("researcher")
			wantCond(t, st, "Applied", "True", "Applied")
			wantCond(t, st, "Ready", "True", "Ready")
			wantCond(t, st, "Synced", "False", "TransientError")
			if endpointURL(st) == "" || st.AgentRef == nil {
				t.Fatalf("the last known state must stay: %+v", st)
			}
			// it recovers
			e.srv.Intercept = nil
			e.rt.readyErr = nil
			e.pass(t)
			wantCond(t, e.status("researcher"), "Synced", "True", "Synced")
		})
	}
}

// ---- M10: per-name memory is dropped ----

func TestPerNameStateIsDroppedWhenADefinitionIsDeleted(t *testing.T) {
	e, _ := newEnv(t)
	e.gw.failReg = errors.New("down")
	e.seed("researcher", nil)
	_ = e.c.ReconcileAll(context.Background())
	e.c.smu.Lock()
	n := len(e.c.last) + len(e.c.fails)
	e.c.smu.Unlock()
	if n == 0 {
		t.Fatal("setup: expected some per-name state")
	}
	e.gw.failReg = nil
	cl := kube.New(e.srv.URL, e.srv.Client(), func() (string, error) { return "t", nil })
	_ = cl.Delete(context.Background(), definitions, "kagent", "researcher")
	e.pass(t)
	e.pass(t)
	e.c.smu.Lock()
	defer e.c.smu.Unlock()
	if len(e.c.last) != 0 || len(e.c.fails) != 0 || len(e.c.seen) != 0 {
		t.Fatalf("state left behind: last=%v fails=%v seen=%v", e.c.last, e.c.fails, e.c.seen)
	}
}
