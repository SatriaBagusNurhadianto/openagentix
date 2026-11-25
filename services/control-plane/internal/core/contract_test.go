package core

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/keys"
	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
	"github.com/openagentix/openagentix/services/control-plane/internal/kubetest"
)

// ---- ADR 0012 change 1 and 2: the gateway user is <namespace>.<name>, and nothing assumes the namespace "kagent" ----

func TestGatewayUserIsNamespaceDotNameAndTheKeySecretUsesTheRuntimesDataKey(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	if _, ok := e.gw.entries["kagent.researcher"]; !ok || len(e.gw.entries) != 1 {
		t.Fatalf("the entry is named <namespace>.<name>: %v", e.gw.entries)
	}
	if e.status("researcher").Controller.GatewayUserID != "kagent.researcher" {
		t.Fatal("status.controller.gatewayUserId")
	}
	sec, _ := e.srv.Get("secrets", "kagent", "researcher-virtual-key-"+uid8(e.raw("researcher")))
	if _, ok := sec["data"].(map[string]any)["KEY"]; !ok || len(sec["data"].(map[string]any)) != 1 {
		t.Fatalf("the data key of the Secret is the runtime adapter's: %v", sec["data"])
	}
}

func TestAnEntryOfTheOldFormatIsSweptAndTheNewOneRegistered(t *testing.T) {
	e, _ := newEnv(t)
	e.gw.entries["researcher"] = "sha256:00000000000000001111111111111111" // written by the previous controller: the plain name
	e.seed("researcher", nil)
	e.pass(t)
	if _, ok := e.gw.entries["researcher"]; ok {
		t.Fatal("the old entry stays")
	}
	if e.gw.entries["kagent.researcher"] != keys0(e) {
		t.Fatalf("%v", e.gw.entries)
	}
	wantCond(t, e.status("researcher"), "Ready", "True", "Ready")
}

// The previous controller registered the key under the plain name and kept its bookkeeping at the top level of status. After the
// upgrade the new controller finds the hash under the old name and no record: it moves the hash to <namespace>.<name> instead of
// replacing a key that is in order (the Secret has the same name, so it holds the key that hash belongs to).
func TestAnEntryOfThePreviousFormatMovesToTheNewUserWithoutReplacingTheKey(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	hash := e.gw.entries["kagent.researcher"]
	// the state the previous controller left: the entry under the plain name, no bookkeeping in status.controller
	delete(e.gw.entries, "kagent.researcher")
	e.gw.entries["researcher"] = hash
	e.srv.Modify("agentdefinitions", "kagent", "researcher", func(o kubetest.Object) { delete(o["status"].(map[string]any), "controller") })
	e.mint = "vk-ffffffffffffffffffffffffffffffff" // a new key would be visible
	secretName := "researcher-virtual-key-" + uid8(e.raw("researcher"))
	before, _ := e.srv.Get("secrets", "kagent", secretName)
	dels := len(e.srv.Matching("DELETE", "/secrets/"))
	e.pass(t)
	if e.gw.entries["kagent.researcher"] != hash {
		t.Fatalf("the hash must move to the new user: %v", e.gw.entries)
	}
	if _, ok := e.gw.entries["researcher"]; ok {
		t.Fatal("the old entry stays")
	}
	// the create is tried (409, the Secret is there) but the Secret is neither deleted nor changed
	after, _ := e.srv.Get("secrets", "kagent", secretName)
	if len(e.srv.Matching("DELETE", "/secrets/")) != dels || fmt.Sprint(before["data"]) != fmt.Sprint(after["data"]) {
		t.Fatal("the key Secret must not be replaced")
	}
	st := e.status("researcher")
	if st.Controller.KeyFingerprint != keys.Fingerprint(hash) || st.Controller.KeySecret == "" {
		t.Fatalf("%+v", st.Controller)
	}
	wantCond(t, st, "Ready", "True", "Ready")
}

func TestSweepKeepsEveryListedDefinitionsUserAndRemovesNoOtherOfItsOwn(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("a", nil)
	e.seed("b", nil)
	e.gw.entries["kagent.gone"] = "sha256:dead"
	e.gw.entries["other-ns.a"] = "sha256:dead" // another namespace's entry is not this definition's user, and not ours to remove (one controller per gateway)
	e.pass(t)
	var got []string
	for u := range e.gw.entries {
		got = append(got, u)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"kagent.a", "kagent.b", "other-ns.a"}) {
		t.Fatalf("%v", got)
	}
}

func TestNothingAssumesTheNamespaceIsKagent(t *testing.T) {
	e, _ := newEnvIn(t, "agents-2")
	e.seed("researcher", nil)
	e.pass(t)
	st := e.status("researcher")
	wantCond(t, st, "Ready", "True", "Ready")
	if st.Controller.GatewayUserID != "agents-2.researcher" || endpointURL(st) != "http://gate/agents/agents-2/researcher" {
		t.Fatalf("%+v", st)
	}
	if _, ok := e.gw.entries["agents-2.researcher"]; !ok {
		t.Fatalf("%v", e.gw.entries)
	}
	for _, r := range e.srv.Requests() {
		if strings.Contains(r.Path, "/namespaces/kagent/") {
			t.Fatalf("a request went to the literal namespace kagent: %s %s", r.Method, r.Path)
		}
	}
	// the objects are written next to the definition, in its namespace
	if len(e.srv.Matching("PATCH", "/namespaces/agents-2/widgets/researcher-w")) != 1 {
		t.Fatal("objects must be written in the definition's namespace")
	}
	// reserved names follow the catalog's namespace: a chart agent's name is reserved in agents-2 now
	e.seed("assistant", nil)
	e.pass(t)
	wantCond(t, e.status("assistant"), "Accepted", "False", "PolicyViolation")
}

// multiNS lists the definitions of two namespaces as one list, as a controller that reconciled several namespaces would
// see them. Everything else goes to the fake API server.
type multiNS struct {
	Kube
	other string
}

func (m multiNS) List(ctx context.Context, r kube.Resource, ns, sel string) ([]kube.Object, string, error) {
	items, rv, err := m.Kube.List(ctx, r, ns, sel)
	if err != nil || r.Plural != "agentdefinitions" {
		return items, rv, err
	}
	more, _, err := m.Kube.List(ctx, r, m.other, sel)
	return append(items, more...), rv, err
}

func TestMaxAgentsAndNamesAreCountedPerNamespace(t *testing.T) {
	e, _ := newEnv(t)
	e.pol.MaxAgents = 1
	e.c.cfg.Kube = multiNS{Kube: e.c.cfg.Kube, other: "team-a"}
	e.seed("one", nil)
	e.seed("two", nil)                                             // second in kagent: over the limit of one per namespace
	e.srv.Seed("agentdefinitions", defObjIn("team-a", "one", nil)) // same name in another namespace: its own place
	e.pass(t)
	wantCond(t, e.status("one"), "Accepted", "True", "Accepted")
	wantCond(t, e.status("two"), "Accepted", "False", "PolicyViolation")
	o, _ := e.srv.Get("agentdefinitions", "team-a", "one")
	d, _ := api.FromObject(o)
	wantCond(t, d.Status, "Accepted", "True", "Accepted")
	if d.Status.Controller.GatewayUserID != "team-a.one" {
		t.Fatalf("%+v", d.Status.Controller)
	}
	if _, ok := e.gw.entries["kagent.one"]; !ok {
		t.Fatalf("%v", e.gw.entries)
	}
	if _, ok := e.gw.entries["team-a.one"]; !ok {
		t.Fatalf("two definitions with one name in two namespaces have two users: %v", e.gw.entries)
	}
	// the objects of the second one went to its own namespace
	if len(e.srv.Matching("PATCH", "/namespaces/team-a/widgets/one-w")) != 1 || len(e.srv.Matching("PATCH", "/namespaces/kagent/widgets/one-w")) != 1 {
		t.Fatalf("%v", e.srv.Requests())
	}
	// and it is deleted in its own namespace
	e.srv.Modify("agentdefinitions", "team-a", "one", func(o kubetest.Object) { kube.Meta(o)["deletionTimestamp"] = "2026-11-25T13:00:00Z" })
	e.pass(t)
	if _, ok := e.gw.entries["team-a.one"]; ok {
		t.Fatal("the entry of the deleted definition stays")
	}
	if _, ok := e.gw.entries["kagent.one"]; !ok {
		t.Fatal("the other namespace's entry must stay")
	}
}

func keys0(e *env) string { return "sha256:" + sha(e.mint) }

// ---- ADR 0012 change 5, 6, 7: the status contract ----

func TestStatusHasThePublicContractAndTheControllerBlockOnly(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	o, _ := e.srv.Get("agentdefinitions", "kagent", "researcher")
	st := o["status"].(map[string]any)
	allowed := map[string]bool{"controller": true}
	for _, k := range api.PublicStatusFields {
		allowed[k] = true
		if _, ok := st[k]; !ok {
			t.Errorf("public status field %q is missing", k)
		}
	}
	for k := range st {
		if !allowed[k] {
			t.Errorf("status.%s is neither in the contract nor under status.controller", k)
		}
	}
	for _, gone := range []string{"a2aUrl", "keySecret", "keyFingerprint", "appliedGeneration", "appliedSpec", "gatewayUserId", "toolServer"} {
		if _, ok := st[gone]; ok {
			t.Errorf("status.%s must not be at the top level", gone)
		}
	}
	ep := st["endpoint"].(map[string]any)
	if ep["protocol"] != "a2a" || ep["url"] != "http://gate/agents/kagent/researcher" || ep["protocolVersion"] != "1.0" {
		t.Fatalf("%v", ep)
	}
	if rev, _ := st["revision"].(string); !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(rev) {
		t.Fatalf("revision %q", rev)
	}
	caps := st["capabilities"].(map[string]any)
	if caps["approvals"] != "yes" || caps["maxStepSeconds"] != float64(60) || caps["cancelStopsLoop"] != "no" {
		t.Fatalf("the core copies the adapter's capabilities: %v", caps)
	}
	ctl := st["controller"].(map[string]any)
	if ctl["gatewayUserId"] != "kagent.researcher" || ctl["toolServer"] != "toolhive-vmcp-lookup" || ctl["appliedGeneration"] != float64(1) || ctl["appliedSpec"] == "" || ctl["keySecret"] == "" || ctl["keyFingerprint"] == "" {
		t.Fatalf("%v", ctl)
	}
}

func TestReadyTrueMeansCallableAtTheEndpointAtTheRevision(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	st := e.status("researcher")
	if st.Condition("Ready").Status != "True" || st.Endpoint == nil || st.Revision == "" {
		t.Fatalf("Ready=True with no endpoint or no revision: %+v", st)
	}
	// no endpoint (the gate is off): the agent is not callable at an endpoint, so Ready is never True
	e2, _ := newEnv(t)
	e2.rt.noEndpoint = true
	e2.seed("researcher", nil)
	e2.pass(t)
	s2 := e2.status("researcher")
	if s2.Endpoint != nil {
		t.Fatal("no endpoint to advertise")
	}
	if s2.Condition("Ready").Status == "True" {
		t.Fatal("Ready=True promises callable at status.endpoint")
	}
	wantCond(t, s2, "Ready", "False", "NoEndpoint")
}

func TestRevisionChangesWithWhatWasAppliedAndNotWithTheDescription(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	r1 := e.status("researcher").Revision
	e.pass(t)
	if e.status("researcher").Revision != r1 {
		t.Fatal("a second pass changed the revision")
	}
	e.srv.Modify("agentdefinitions", "kagent", "researcher", func(o kubetest.Object) { o["spec"].(map[string]any)["description"] = "new words" })
	e.pass(t)
	if e.status("researcher").Revision != r1 {
		t.Fatal("a description change must not change the revision")
	}
	e.srv.Modify("agentdefinitions", "kagent", "researcher", func(o kubetest.Object) { o["spec"].(map[string]any)["instructions"] = "Be even briefer.\n" })
	e.pass(t)
	if e.status("researcher").Revision == r1 {
		t.Fatal("an instructions change must change the revision")
	}
}

// ---- change 7 and 8 of ADR 0012: suspend ----

func TestSuspendKeepsTheDefinitionAcceptedAndRemovesOnlyTheAgent(t *testing.T) {
	e, _ := newEnv(t)
	clk := e.useClock()
	e.seed("researcher", nil)
	e.pass(t)
	rev := e.status("researcher").Revision
	secret := "researcher-virtual-key-" + uid8(e.raw("researcher"))
	e.srv.Modify("agentdefinitions", "kagent", "researcher", func(o kubetest.Object) { o["spec"].(map[string]any)["suspend"] = true })
	e.pass(t)
	st := e.status("researcher")
	wantCond(t, st, "Accepted", "True", "Accepted")
	wantCond(t, st, "Applied", "True", "Applied")
	wantCond(t, st, "Ready", "False", "Suspended")
	if st.Endpoint != nil || st.AgentRef != nil {
		t.Fatalf("a suspended agent has no endpoint and no agent object to refer to: %+v", st)
	}
	if st.Revision != rev || len(st.EffectiveTools) != 1 {
		t.Fatalf("the revision and the effective tools still describe what is configured: %+v", st)
	}
	if _, ok := e.srv.Get("widgets", "kagent", "researcher-w"); ok {
		t.Fatal("the agent object must be gone")
	}
	if _, ok := e.srv.Get("gadgets", "kagent", "researcher-g"); !ok {
		t.Fatal("the other objects stay")
	}
	if _, ok := e.srv.Get("secrets", "kagent", secret); !ok {
		t.Fatal("the key Secret stays")
	}
	if _, ok := e.gw.entries["kagent.researcher"]; ok {
		t.Fatal("the registration is cut while suspended (see TestSuspendCutsModelAccess...)")
	}
	if len(e.srv.Matching("DELETE", "/secrets/")) != 0 {
		t.Fatal("suspend must not delete the key")
	}
	// a steady state writes nothing more
	patches := len(e.srv.Matching("PATCH", "/agentdefinitions/researcher/status"))
	e.pass(t)
	if got := len(e.srv.Matching("PATCH", "/agentdefinitions/researcher/status")); got != patches {
		t.Fatalf("status patched again while suspended: %d -> %d", patches, got)
	}
	// resume
	clk.advance(10 * time.Minute)
	e.srv.Modify("agentdefinitions", "kagent", "researcher", func(o kubetest.Object) { o["spec"].(map[string]any)["suspend"] = false })
	e.pass(t)
	st = e.status("researcher")
	wantCond(t, st, "Ready", "True", "Ready")
	if st.Endpoint == nil || st.AgentRef == nil || st.Revision != rev {
		t.Fatalf("%+v", st)
	}
	if _, ok := e.srv.Get("widgets", "kagent", "researcher-w"); !ok {
		t.Fatal("the agent object must be back")
	}
}

func TestSuspendAllOfThePolicyIsTheSameForEveryDefinition(t *testing.T) {
	e, _ := newEnv(t)
	clk := e.useClock()
	e.seed("a", nil)
	e.seed("b", nil)
	e.pass(t)
	e.pol.SuspendAll = true
	e.pass(t)
	for _, n := range []string{"a", "b"} {
		st := e.status(n)
		wantCond(t, st, "Accepted", "True", "Accepted")
		wantCond(t, st, "Ready", "False", "Suspended")
		if !strings.Contains(st.Condition("Ready").Message, "operator") {
			t.Errorf("%s: the message says who suspended it: %q", n, st.Condition("Ready").Message)
		}
		if _, ok := e.srv.Get("widgets", "kagent", n+"-w"); ok {
			t.Errorf("%s: agent object still there", n)
		}
	}
	// a definition created while the switch is on is suspended from the start, and never runs
	e.seed("c", nil)
	e.pass(t)
	wantCond(t, e.status("c"), "Ready", "False", "Suspended")
	if _, ok := e.srv.Get("widgets", "kagent", "c-w"); ok {
		t.Fatal("a new definition must not start an agent")
	}
	clk.advance(10 * time.Minute)
	e.pol.SuspendAll = false
	e.pass(t)
	wantCond(t, e.status("a"), "Ready", "True", "Ready")
}

func TestSuspendAllAlsoStopsADefinitionWhoseEditIsRefused(t *testing.T) {
	e, _ := newEnv(t)
	e.seed("researcher", nil)
	e.pass(t)
	e.srv.Modify("agentdefinitions", "kagent", "researcher", func(o kubetest.Object) { o["spec"].(map[string]any)["model"] = "gpt-9" })
	e.pol.SuspendAll = true
	e.pass(t)
	st := e.status("researcher")
	wantCond(t, st, "Accepted", "False", "PolicyViolation")
	if _, ok := e.srv.Get("widgets", "kagent", "researcher-w"); ok {
		t.Fatal("the stop switch stops the previous generation too")
	}
	wantCond(t, st, "Ready", "False", "Suspended")
}

// ---- change 3, 4, 11 of ADR 0012: reserved shapes are refused, not ignored ----

func TestKnowledgeCollectionsNotifyDestinationsAndInstructionsFromAreRefusedWithAClearSentence(t *testing.T) {
	for name, mut := range map[string]func(map[string]any){
		"collections": func(s map[string]any) {
			s["knowledge"] = map[string]any{"enabled": false, "collections": []any{"docs"}}
		},
		"destinations": func(s map[string]any) { s["notify"] = map[string]any{"destinations": []any{"chat"}} },
		"instructionsFrom": func(s map[string]any) {
			delete(s, "instructions")
			s["instructionsFrom"] = map[string]any{"configMap": map[string]any{"name": "p", "key": "k"}}
		},
	} {
		e, _ := newEnv(t)
		e.seed("r", mut)
		e.pass(t)
		st := e.status("r")
		wantCond(t, st, "Accepted", "False", "PolicyViolation")
		if len(st.Violations) == 0 || !strings.Contains(st.Violations[0], "not available yet") {
			t.Errorf("%s: %q", name, st.Violations)
		}
		if _, ok := e.srv.Get("widgets", "kagent", "r-w"); ok {
			t.Errorf("%s: nothing may be built", name)
		}
	}
}

// ---- change 12: reserved labels ----

func TestTeamAndProjectLabelsReachTheObjectsAndTheGatewayEntry(t *testing.T) {
	e, _ := newEnv(t)
	o := defObj("researcher", nil)
	kube.Meta(o)["labels"] = map[string]any{api.LabelTeam: "payments", api.LabelProject: "ledger", api.LabelWorkItem: "wi-1"}
	e.srv.Seed("agentdefinitions", o)
	e.pass(t)
	wantCond(t, e.status("researcher"), "Ready", "True", "Ready")
	for _, plural := range []string{"widgets/researcher-w", "gadgets/researcher-g"} {
		g, _ := e.srv.Get(strings.Split(plural, "/")[0], "kagent", strings.Split(plural, "/")[1])
		l := kube.Meta(g)["labels"].(map[string]any)
		if l[api.LabelTeam] != "payments" || l[api.LabelProject] != "ledger" {
			t.Errorf("%s labels: %v", plural, l)
		}
		if _, ok := l[api.LabelWorkItem]; ok {
			t.Errorf("%s: work-item is not copied", plural)
		}
	}
	if got := e.gw.labels["kagent.researcher"]; got[api.LabelTeam] != "payments" || got[api.LabelProject] != "ledger" || len(got) != 2 {
		t.Fatalf("gateway entry labels: %v", got)
	}
	// a new team label rewrites the entry and touches no key
	creates := len(e.srv.Matching("POST", "/secrets"))
	e.srv.Modify("agentdefinitions", "kagent", "researcher", func(o kubetest.Object) { kube.Meta(o)["labels"].(map[string]any)[api.LabelTeam] = "risk" })
	e.pass(t)
	if got := e.gw.labels["kagent.researcher"][api.LabelTeam]; got != "risk" {
		t.Fatalf("the entry keeps the old team: %q", got)
	}
	if len(e.srv.Matching("POST", "/secrets")) != creates || len(e.srv.Matching("DELETE", "/secrets/")) != 0 {
		t.Fatal("a label change must not replace the key")
	}
}

func TestLabelsNeverDecideAcceptance(t *testing.T) {
	e, _ := newEnv(t)
	o := defObj("bad", func(s map[string]any) { s["model"] = "gpt-9" })
	kube.Meta(o)["labels"] = map[string]any{api.LabelTeam: "admin", api.LabelProject: "root", api.LabelWorkItem: "everything-allowed"}
	e.srv.Seed("agentdefinitions", o)
	e.pass(t)
	wantCond(t, e.status("bad"), "Accepted", "False", "PolicyViolation")
}

// ---- change 8: every reason the code sets is in the table ----

// The condition table (api/reasons.go) is the vocabulary. This reads core.go and fails when a call that sets a condition uses
// a reason that is not in the table for that condition type; a reason that is not a literal must be one of the helpers that
// map it into the table first.
func TestEveryReasonTheCoreSetsIsInTheTable(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "core.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	typeOf := func(e ast.Expr) string {
		if se, ok := e.(*ast.SelectorExpr); ok {
			switch se.Sel.Name {
			case "CondAccepted":
				return api.CondAccepted
			case "CondKeyReady":
				return api.CondKeyReady
			case "CondApplied":
				return api.CondApplied
			case "CondReady":
				return api.CondReady
			case "CondSynced":
				return api.CondSynced
			}
		}
		return ""
	}
	lit := func(e ast.Expr) (string, bool) {
		if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.STRING {
			s, _ := strconv.Unquote(b.Value)
			return s, true
		}
		return "", false
	}
	checked := 0
	// the functions that map a runtime-supplied reason into the table before setting it
	mapped := map[string]bool{"setReadyFromRuntime": true, "setGen": true, "set": true, "unknownIfAbsent": true}
	var fn string
	ast.Inspect(f, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok {
			fn = fd.Name.Name
			return true
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		var typ, reasonArg = "", ast.Expr(nil)
		switch sel.Sel.Name {
		case "set", "setGen":
			if len(call.Args) >= 3 {
				typ, reasonArg = typeOf(call.Args[0]), call.Args[2]
			}
		case "unknownIfAbsent":
			if len(call.Args) >= 2 {
				typ, reasonArg = typeOf(call.Args[0]), call.Args[1]
			}
		default:
			return true
		}
		if typ == "" {
			if !mapped[fn] { // the helpers take the type as a parameter; their callers are checked
				t.Errorf("%s: a condition set with a type that is not an api.Cond* constant", fset.Position(call.Pos()))
			}
			return true
		}
		r, isLit := lit(reasonArg)
		if !isLit {
			if !mapped[fn] {
				t.Errorf("%s: reason of %s is not a literal and %s is not a function that maps it into the table", fset.Position(call.Pos()), typ, fn)
			}
			return true
		}
		checked++
		if !api.ValidReason(typ, r) {
			t.Errorf("%s: the code sets %s reason %q, which is not in the table (api/reasons.go)", fset.Position(call.Pos()), typ, r)
		}
		return true
	})
	if checked < 20 {
		t.Fatalf("the scan looked at only %d calls: the test is broken", checked)
	}
}

func TestARuntimeReasonOutsideTheTableIsMappedIntoIt(t *testing.T) {
	e, _ := newEnv(t)
	e.rt.ready = readiness(false, "SomethingTheRuntimeSaid", "details")
	e.seed("researcher", nil)
	e.pass(t)
	c := e.status("researcher").Condition("Ready")
	if c.Reason != "RuntimeNotReady" || !strings.Contains(c.Message, "SomethingTheRuntimeSaid") {
		t.Fatalf("%+v", c)
	}
}

// ---- I5 of the security review: suspend is a STOP that also cuts model access, and flapping is rate-limited ----

// clock is a settable time source for the controller under test.
type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func (e *env) useClock() *clock {
	c := &clock{t: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	e.c.cfg.Now = c.Now
	return c
}

func (e *env) suspend(name string, on bool) {
	e.srv.Modify("agentdefinitions", "kagent", name, func(o kubetest.Object) { o["spec"].(map[string]any)["suspend"] = on })
}

func TestSuspendCutsModelAccessAndResumeRestoresTheSameKeyWithoutMintingOne(t *testing.T) {
	e, _ := newEnv(t)
	clk := e.useClock()
	e.seed("researcher", nil)
	e.pass(t)
	hash := e.gw.entries["kagent.researcher"]
	secret := "researcher-virtual-key-" + uid8(e.raw("researcher"))
	rev, fp := e.status("researcher").Revision, e.ctl("researcher")["keyFingerprint"]
	posts, deletes := len(e.srv.Matching("POST", "/secrets")), len(e.srv.Matching("DELETE", "/secrets/"))

	e.suspend("researcher", true)
	e.pass(t)
	if _, ok := e.gw.entries["kagent.researcher"]; ok {
		t.Fatal("a suspended agent's key must not work at the gateway: its entry is removed")
	}
	if got := e.ctl("researcher")["heldKeyHash"]; got != hash {
		t.Fatalf("the non-secret hash is kept in status.controller.heldKeyHash so the same key can come back: %v want %v", got, hash)
	}
	if _, ok := e.srv.Get("secrets", "kagent", secret); !ok {
		t.Fatal("the key Secret stays")
	}
	st := e.status("researcher")
	wantCond(t, st, "Ready", "False", "Suspended")
	wantCond(t, st, "KeyReady", "False", "KeySuspended")
	if _, ok := e.srv.Get("widgets", "kagent", "researcher-w"); ok {
		t.Fatal("the runtime agent is removed: the runtime has no pause, so suspend is a stop")
	}

	// a steady state writes nothing
	regs, patches := e.gw.registerCalls(), len(e.srv.Matching("PATCH", "/agentdefinitions/researcher/status"))
	e.pass(t)
	if e.gw.registerCalls() != regs || len(e.srv.Matching("PATCH", "/agentdefinitions/researcher/status")) != patches {
		t.Fatal("a pass over a suspended definition must change nothing")
	}
	if _, ok := e.gw.entries["kagent.researcher"]; ok {
		t.Fatal("the entry must stay cut")
	}

	// resume: the same hash is registered again; no key is minted, no Secret is written
	clk.advance(10 * time.Minute)
	e.suspend("researcher", false)
	e.pass(t)
	if e.gw.entries["kagent.researcher"] != hash {
		t.Fatalf("resume must register the same hash: %q want %q", e.gw.entries["kagent.researcher"], hash)
	}
	if len(e.srv.Matching("POST", "/secrets")) != posts || len(e.srv.Matching("DELETE", "/secrets/")) != deletes {
		t.Fatal("resume must not create or delete a key Secret")
	}
	st = e.status("researcher")
	wantCond(t, st, "Ready", "True", "Ready")
	wantCond(t, st, "KeyReady", "True", "KeyReady")
	if st.Revision != rev || e.ctl("researcher")["keyFingerprint"] != fp || e.ctl("researcher")["heldKeyHash"] != "" {
		t.Fatalf("same definition, key and revision: %+v %v", st, e.ctl("researcher"))
	}
	if _, ok := e.srv.Get("widgets", "kagent", "researcher-w"); !ok {
		t.Fatal("the agent is back")
	}
}

func TestAKeyThatIsHeldIsNeverRegisteredByAFreshDefinitionCreatedWhileSuspendAllIsOn(t *testing.T) {
	e, _ := newEnv(t)
	clk := e.useClock()
	e.pol.SuspendAll = true
	e.seed("c", nil)
	e.pass(t)
	if _, ok := e.gw.entries["kagent.c"]; ok || e.gw.registerCalls() != 0 {
		t.Fatalf("a definition created while the stop switch is on never gets a registered key: %v", e.gw.entries)
	}
	if e.ctl("c")["heldKeyHash"] != "sha256:"+sha(e.mint) {
		t.Fatalf("the key exists (its Secret) and its hash is held: %v", e.ctl("c"))
	}
	wantCond(t, e.status("c"), "Ready", "False", "Suspended")
	clk.advance(time.Minute)
	e.pol.SuspendAll = false
	e.pass(t)
	if e.gw.entries["kagent.c"] != "sha256:"+sha(e.mint) {
		t.Fatalf("on resume the held hash is registered: %v", e.gw.entries)
	}
	wantCond(t, e.status("c"), "Ready", "True", "Ready")
	if n := len(e.srv.Matching("POST", "/secrets")); n != 1 {
		t.Fatalf("one key was ever minted, got %d Secret creates", n)
	}
}

func TestSuspendAllCutsEveryDefinitionsKeyAndAnEditThatIsRefusedDoesNotKeepItOpen(t *testing.T) {
	e, _ := newEnv(t)
	e.useClock()
	e.seed("a", nil)
	e.seed("b", nil)
	e.pass(t)
	e.srv.Modify("agentdefinitions", "kagent", "b", func(o kubetest.Object) { o["spec"].(map[string]any)["model"] = "gpt-9" }) // b's edit is refused, its previous generation runs
	e.pol.SuspendAll = true
	e.pass(t)
	for _, n := range []string{"a", "b"} {
		if _, ok := e.gw.entries["kagent."+n]; ok {
			t.Errorf("%s: the stop switch must cut the key too", n)
		}
		if e.ctl(n)["heldKeyHash"] == "" {
			t.Errorf("%s: the hash must be held", n)
		}
		if _, ok := e.srv.Get("widgets", "kagent", n+"-w"); ok {
			t.Errorf("%s: the agent must be gone", n)
		}
	}
}

func TestSuspendResumeLoopsAreRateLimitedPerDefinition(t *testing.T) {
	e, _ := newEnv(t)
	clk := e.useClock()
	e.seed("researcher", nil)
	e.pass(t)
	hash := e.gw.entries["kagent.researcher"]

	clk.advance(30 * time.Second)
	e.suspend("researcher", true)
	e.pass(t)
	clk.advance(10 * time.Second)
	e.suspend("researcher", false) // resumed 40 s after the agent was created: too soon
	e.pass(t)
	st := e.status("researcher")
	wantCond(t, st, "Ready", "False", "ResumeDelayed")
	if !strings.Contains(st.Condition("Ready").Message, "2m0s") || !strings.Contains(st.Condition("Ready").Message, "12:02:00") {
		t.Fatalf("the message says the interval and when it ends: %q", st.Condition("Ready").Message)
	}
	if _, ok := e.srv.Get("widgets", "kagent", "researcher-w"); ok {
		t.Fatal("no agent is created inside the interval")
	}
	if _, ok := e.gw.entries["kagent.researcher"]; ok {
		t.Fatal("the key stays cut while the resume is delayed")
	}
	wantCond(t, st, "Accepted", "True", "Accepted")

	clk.advance(81 * time.Second) // 12:02:01
	e.pass(t)
	wantCond(t, e.status("researcher"), "Ready", "True", "Ready")
	if e.gw.entries["kagent.researcher"] != hash {
		t.Fatal("the same key is back")
	}

	// the next cycle starts its own interval, from the re-creation
	clk.advance(5 * time.Second)
	e.suspend("researcher", true)
	e.pass(t)
	e.suspend("researcher", false)
	e.pass(t)
	wantCond(t, e.status("researcher"), "Ready", "False", "ResumeDelayed")
}

func TestAFirstStartIsNeverDelayedAndTheIntervalIsConfigurable(t *testing.T) {
	e, _ := newEnv(t)
	clk := e.useClock()
	e.c.cfg.MinRecreateInterval = time.Second
	e.seed("researcher", nil)
	e.pass(t)
	wantCond(t, e.status("researcher"), "Ready", "True", "Ready")
	e.suspend("researcher", true)
	e.pass(t)
	clk.advance(2 * time.Second)
	e.suspend("researcher", false)
	e.pass(t)
	wantCond(t, e.status("researcher"), "Ready", "True", "Ready")
}
