package guardtest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	utiljson "k8s.io/apimachinery/pkg/util/json"
)

type m = map[string]any

// deepCopy copies through JSON the way the API server's unstructured objects are decoded: whole numbers become int64, not float64
// (a CEL expression sees an int, as it does on a real server).
func deepCopy(v m) m {
	b, _ := json.Marshal(v)
	var out m
	if err := utiljson.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

func clone(v any) any {
	b, _ := json.Marshal(v)
	var out any
	if err := utiljson.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

// walk returns the container that holds the last segment of path, and that segment. Segments are separated by "/" (label
// keys hold dots); a segment that is a number indexes a list.
func walk(o any, path string) (any, string) {
	segs := strings.Split(path, "/")
	cur := o
	for _, s := range segs[:len(segs)-1] {
		cur = child(cur, s)
		if cur == nil {
			panic("path not found: " + path)
		}
	}
	return cur, segs[len(segs)-1]
}

func child(c any, s string) any {
	switch x := c.(type) {
	case m:
		return x[s]
	case []any:
		i, _ := strconv.Atoi(s)
		return x[i]
	}
	return nil
}

// get reads a path.
func get(o any, path string) any {
	c, last := walk(o, path)
	return child(c, last)
}

// put sets a path (a list index must exist; a map key may be new).
func put(o m, path string, v any) {
	c, last := walk(o, path)
	switch x := c.(type) {
	case m:
		x[last] = v
	case []any:
		i, _ := strconv.Atoi(last)
		x[i] = v
	}
}

// drop removes a map key.
func setLabel(o m, k string, v any) { o["metadata"].(m)["labels"].(m)[k] = v }
func delLabel(o m, k string)        { delete(o["metadata"].(m)["labels"].(m), k) }

func drop(o m, path string) {
	c, last := walk(o, path)
	if x, ok := c.(m); ok {
		delete(x, last)
	}
}

const (
	lblManaged = "app.kubernetes.io/managed-by"
	lblDef     = "openagentix.io/agent-definition"
	hex8       = "1a2b3c4d"
)

var plural = map[string]string{"ModelConfig": "modelconfigs", "Harness": "harnesses", "AgentTemplate": "agenttemplates", "EgressPolicy": "egresspolicies", "Agent": "agents"}
var kinds = []string{"ModelConfig", "Harness", "AgentTemplate", "EgressPolicy", "Agent"}

func owner(a string) m {
	return m{"apiVersion": "openagentix.io/v1alpha1", "kind": "AgentDefinition", "name": a, "uid": "1a2b3c4d-0000-4000-8000-000000000000", "controller": true}
}

func stamp(o m, a string) {
	md := o["metadata"].(m)
	md["labels"] = m{lblManaged: "openagentix-control-plane", lblDef: a}
	md["ownerReferences"] = []any{owner(a)}
}

// fromGolden turns the objects `helm template` renders for a chart agent (which the adapter reproduces field for field, a Go test
// compares them) into what the controller writes for the definition dst: names replaced, the Helm labels gone, the controller's
// labels, owner reference and key fingerprint on, the key Secret of the ModelConfig named for the definition.
func fromGolden(t *testing.T, file, src, dst, mc string) []m {
	var objs []m
	readJSON(t, "services/control-plane/testdata/"+file, &objs)
	var out []m
	for _, o := range objs {
		b, _ := json.Marshal(o)
		s := strings.ReplaceAll(strings.ReplaceAll(string(b), mc, "gateway-"+dst), src, dst)
		var n m
		_ = json.Unmarshal([]byte(s), &n)
		md := n["metadata"].(m)
		delete(md, "labels")
		switch n["kind"] {
		case "ModelConfig":
			n["spec"].(m)["apiKeySecret"] = dst + "-virtual-key-" + hex8
		case "Harness", "Agent":
			md["annotations"] = m{"openagentix.io/key-fingerprint": "0123456789abcdef"}
		}
		stamp(n, dst)
		out = append(out, n)
	}
	return out
}

func find(objs []m, kind string) m {
	for _, o := range objs {
		if o["kind"] == kind {
			return deepCopy(o)
		}
	}
	panic("no " + kind)
}

func secret(a string) m {
	return m{"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "immutable": true,
		"metadata": m{"name": a + "-virtual-key-" + hex8, "namespace": "kagent",
			"labels":          m{lblManaged: "openagentix-control-plane", lblDef: a},
			"annotations":     m{"openagentix.io/key-fingerprint": "0123"},
			"ownerReferences": []any{owner(a)}},
		"data": m{"OPENAI_API_KEY": "dmstZmFrZQ=="}}
}

func entry(user, hash, team, project string) string {
	meta := `"user_id":"` + user + `"`
	if team != "" {
		meta += `,"team":"` + team + `"`
	}
	if project != "" {
		meta += `,"project":"` + project + `"`
	}
	return `{"keyHash":"` + hash + `","metadata":{` + meta + `}}`
}

var hash64 = "sha256:" + strings.Repeat("ab12", 16)

func keyMap(data m) m {
	return m{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": m{"name": "llm-api-keys-control-plane", "namespace": "agentgateway-system", "labels": m{"agentgateway.dev/apikey": "true", "app.kubernetes.io/part-of": "openagentix"}},
		"data":     data}
}

type egressTable struct {
	Namespaces         struct{ Agents, Gateway, Tools string }
	InCluster          []string
	ForbiddenInCluster []string
	Cases              []struct {
		Origin string
		Allow  bool
		Why    string
	}
}

const (
	controller = "system:serviceaccount:" + releaseNamespace + ":openagentix-control-plane"
	allow      = true
	deny       = false
)

// server fills what the API server adds to an object (and what another controller may add) so that an UPDATE looks real.
func server(o m) m {
	n := deepCopy(o)
	md := n["metadata"].(m)
	md["uid"] = "2b3c4d5e-1111-4222-8333-444455556666"
	md["resourceVersion"] = "12345"
	md["generation"] = float64(2)
	md["creationTimestamp"] = "2026-11-25T10:00:00Z"
	md["finalizers"] = []any{"kagent.dev/finalizer"}
	md["managedFields"] = []any{m{"manager": "openagentix-control-plane", "operation": "Apply"}}
	n["status"] = m{"observedGeneration": float64(2), "conditions": []any{}}
	return n
}

// expectation is a request and what the guard must say.
type expectation struct {
	req   request
	allow bool
	// unmatched: no policy of the guard is meant to select the request (it is another user, or a resource the guard leaves to RBAC)
	unmatched bool
}

type builder struct {
	t     *testing.T
	r     *rendered
	out   []expectation
	agent string
	cat   catalogFile
}

func (b *builder) add(name, op, group, res string, obj, old m, ok bool) {
	ns := "kagent"
	n := ""
	src := obj
	if src == nil {
		src = old
	}
	if md, _ := src["metadata"].(m); md != nil {
		n, _ = md["name"].(string)
		if x, _ := md["namespace"].(string); x != "" {
			ns = x
		}
	}
	// the request keeps its own copy: a case may go on mutating the object it was built from
	if obj != nil {
		obj = deepCopy(obj)
	}
	if old != nil {
		old = deepCopy(old)
	}
	b.out = append(b.out, expectation{req: request{Name: name, User: controller, Op: op, Group: group, Resource: res, Namespace: ns, ObjName: n, Object: obj, Old: old}, allow: ok})
}

func (b *builder) kagent(name, op string, kind string, obj, old m, ok bool) {
	b.add(name, op, "api.kagent.dev", plural[kind], obj, old, ok)
}

// buildCases returns every request of the suite.
func buildCases(t *testing.T, r *rendered, eg egressTable) []expectation {
	b := &builder{t: t, r: r, cat: r.Catalog}
	cat := r.Catalog
	if len(cat.Tools.Toolhive.Profiles) == 0 || len(cat.Models) == 0 {
		t.Fatal("the demo render has no model or no tool profile in the catalog")
	}
	prof := cat.Tools.Toolhive.Profiles[0]

	A := fromGolden(t, "golden-profile-demo.json", "profile-demo", "researcher", "gateway-profile-demo")
	H := fromGolden(t, "golden-assistant.json", "assistant", "helper", "gateway-claude-haiku")
	// the golden assistant uses the shared tool server and every tool; a definition uses the server of its profile and the profile's tools
	for _, o := range H {
		if o["kind"] == "AgentTemplate" {
			s := o["spec"].(m)
			s["tools"] = []any{m{"mcp": m{"server": m{"kind": "RemoteMCPServer", "name": prof.ToolServer}, "tools": strs(prof.Tools)}}}
		}
	}
	var harnessEnv []any
	for _, e := range cat.Runtime.Kagent.HarnessEnv {
		harnessEnv = append(harnessEnv, m{"name": e.Name, "value": e.Value})
	}
	if len(harnessEnv) == 0 {
		t.Fatal("the render's catalog has no harness env: the case below would test nothing")
	}

	// ---- allowed: what the controller writes, every kind, every operation
	for _, set := range []struct {
		tag  string
		objs []m
	}{{"researcher", A}, {"helper", H}} {
		for _, o := range set.objs {
			k := o["kind"].(string)
			b.kagent(fmt.Sprintf("allow %s %s CREATE", set.tag, k), "CREATE", k, deepCopy(o), nil, allow)
			b.kagent(fmt.Sprintf("allow %s %s UPDATE", set.tag, k), "UPDATE", k, deepCopy(o), deepCopy(o), allow)
			b.kagent(fmt.Sprintf("allow %s %s UPDATE with what the server and kagent add", set.tag, k), "UPDATE", k, server(o), server(o), allow)
			b.kagent(fmt.Sprintf("allow %s %s DELETE", set.tag, k), "DELETE", k, nil, server(o), allow)
		}
	}
	{
		h := find(H, "Harness")
		put(h, "spec/env", clone(harnessEnv))
		b.kagent("allow Harness with the catalog's env", "CREATE", "Harness", h, nil, allow)
		mc := find(A, "ModelConfig")
		put(mc, "spec/openAI/apiFormat", "chatCompletions")
		b.kagent("allow ModelConfig with the CRD default openAI.apiFormat", "CREATE", "ModelConfig", mc, nil, allow)
		mc = find(A, "ModelConfig")
		setLabel(mc, "openagentix.io/team", "platform")
		setLabel(mc, "openagentix.io/project", "demo")
		b.kagent("allow the reserved team and project labels", "CREATE", "ModelConfig", mc, nil, allow)
		tp := find(A, "AgentTemplate")
		put(tp, "spec/tools", []any{m{"mcp": m{"server": m{"kind": "RemoteMCPServer", "name": prof.ToolServer}, "tools": []any{prof.Tools[0]}}}})
		b.kagent("allow an AgentTemplate with a subset of the profile's tools", "CREATE", "AgentTemplate", tp, nil, allow)
		tp = find(A, "AgentTemplate")
		drop(tp, "spec/tools")
		b.kagent("allow an AgentTemplate without tools", "CREATE", "AgentTemplate", tp, nil, allow)
	}
	// secrets
	for _, a := range []string{"researcher", "a", "my-agent-2"} {
		s := secret(a)
		b.add("allow Secret CREATE "+a, "CREATE", "", "secrets", s, nil, allow)
		b.add("allow Secret DELETE "+a, "DELETE", "", "secrets", nil, deepCopy(s), allow)
	}
	// the key ConfigMap
	{
		good := m{"kagent.researcher": entry("kagent.researcher", hash64, "platform", "demo"), "kagent.a": entry("kagent.a", hash64, "", "")}
		old := m{"kagent.researcher": entry("kagent.researcher", hash64, "", "")}
		b.add("allow the key ConfigMap UPDATE with well-formed entries", "UPDATE", "", "configmaps", keyMap(good), keyMap(old), allow)
		b.add("allow the key ConfigMap UPDATE removing every entry", "UPDATE", "", "configmaps", keyMap(m{}), keyMap(old), allow)
		legacy := m{"researcher": `{"keyHash":"sha256:` + strings.Repeat("0", 64) + `","metadata":{"user_id":"researcher"}}`}
		both := m{"researcher": legacy["researcher"], "kagent.researcher": entry("kagent.researcher", hash64, "", "")}
		b.add("allow the key ConfigMap UPDATE that leaves an entry of the previous format untouched", "UPDATE", "", "configmaps", keyMap(both), keyMap(legacy), allow)
	}
	// egress hosts: the table the Go check uses too
	for _, c := range eg.Cases {
		e := find(A, "EgressPolicy")
		put(e, "spec/destinations", []any{m{"origin": c.Origin}})
		b.kagent("egress "+c.Origin+" ("+c.Why+")", "CREATE", "EgressPolicy", e, nil, c.Allow)
	}
	{
		e := find(A, "EgressPolicy")
		put(e, "spec/destinations", []any{m{"origin": "https://api.osv.dev"}, m{"origin": "https://b.example.com"}})
		b.kagent("allow two public destinations", "CREATE", "EgressPolicy", e, nil, allow)
		put(e, "spec/destinations", []any{m{"origin": "https://api.osv.dev"}, m{"origin": "https://x.svc"}})
		b.kagent("deny: a second destination inside the cluster", "CREATE", "EgressPolicy", e, nil, deny)
	}
	// not the controller, and what the guard leaves alone
	b.out = append(b.out,
		expectation{req: request{Name: "other user: any Secret write", User: "alice", Op: "CREATE", Group: "", Resource: "secrets", Namespace: "kagent", ObjName: "x", Object: m{"metadata": m{"name": "x"}}}, allow: true, unmatched: true},
		expectation{req: request{Name: "other user: delete any Secret", User: "alice", Op: "DELETE", Group: "", Resource: "secrets", Namespace: "kagent", ObjName: "kagent-postgresql", Old: m{"metadata": m{"name": "kagent-postgresql"}}}, allow: true, unmatched: true},
		expectation{req: request{Name: "other ServiceAccount: write a ConfigMap", User: "system:serviceaccount:kagent:kagent-controller", Op: "UPDATE", Resource: "configmaps", Namespace: "kagent", ObjName: "x", Object: m{"metadata": m{"name": "x"}}, Old: m{"metadata": m{"name": "x"}}}, allow: true, unmatched: true},
		expectation{req: request{Name: "other user: any kagent object", User: "bob", Op: "CREATE", Group: "api.kagent.dev", Resource: "agents", Namespace: "kagent", ObjName: "x", Object: m{"metadata": m{"name": "x"}}}, allow: true, unmatched: true},
		expectation{req: request{Name: "other user: any pod", User: "bob", Op: "CREATE", Resource: "pods", Namespace: "kagent", ObjName: "x", Object: m{"metadata": m{"name": "x"}}}, allow: true, unmatched: true},
		// the controller's own definitions: the finalizer (an UPDATE) and the status (a patch on the subresource) are its business
		expectation{req: request{Name: "allow the controller to update a definition (the finalizer)", User: controller, Op: "UPDATE", Group: "openagentix.io", Resource: "agentdefinitions", Namespace: "kagent", ObjName: "researcher", Object: m{"metadata": m{"name": "researcher"}}, Old: m{"metadata": m{"name": "researcher"}}}, allow: true, unmatched: true},
		expectation{req: request{Name: "allow the controller to patch a definition's status", User: controller, Op: "UPDATE", Group: "openagentix.io", Resource: "agentdefinitions", Sub: "status", Namespace: "kagent", ObjName: "researcher", Object: m{"metadata": m{"name": "researcher"}}, Old: m{"metadata": m{"name": "researcher"}}}, allow: true, unmatched: true},
	)

	// ---- denied: Secrets
	sm := func(name string, f func(o m)) {
		o := secret("researcher")
		f(o)
		b.add("deny Secret: "+name, "CREATE", "", "secrets", o, nil, deny)
	}
	sm("no suffix", func(o m) { put(o, "metadata/name", "researcher-virtual-key") })
	sm("7 hex", func(o m) { put(o, "metadata/name", "researcher-virtual-key-1a2b3c4") })
	sm("9 hex", func(o m) { put(o, "metadata/name", "researcher-virtual-key-1a2b3c4d5") })
	sm("uppercase hex", func(o m) { put(o, "metadata/name", "researcher-virtual-key-1A2B3C4D") })
	sm("foreign name", func(o m) { put(o, "metadata/name", "default-model-config") })
	sm("name does not match the label prefix", func(o m) { put(o, "metadata/name", "other-virtual-key-1a2b3c4d") })
	sm("leading dash", func(o m) { put(o, "metadata/name", "-virtual-key-1a2b3c4d") })
	sm("no managed-by label", func(o m) { delLabel(o, lblManaged) })
	sm("no agent-definition label", func(o m) { delLabel(o, lblDef) })
	sm("wrong managed-by value", func(o m) { setLabel(o, lblManaged, "Helm") })
	sm("no labels at all", func(o m) { drop(o, "metadata/labels") })
	sm("no ownerReferences", func(o m) { drop(o, "metadata/ownerReferences") })
	sm("two ownerReferences", func(o m) {
		md := o["metadata"].(m)
		md["ownerReferences"] = append(md["ownerReferences"].([]any), owner("researcher"))
	})
	sm("ownerRef kind Pod", func(o m) { put(o, "metadata/ownerReferences/0/kind", "Pod") })
	sm("ownerRef controller false", func(o m) { put(o, "metadata/ownerReferences/0/controller", false) })
	sm("ownerRef controller absent", func(o m) { drop(o, "metadata/ownerReferences/0/controller") })
	sm("ownerRef other group", func(o m) { put(o, "metadata/ownerReferences/0/apiVersion", "v1") })
	sm("ownerRef name differs", func(o m) { put(o, "metadata/ownerReferences/0/name", "other") })
	sm("not immutable", func(o m) { put(o, "immutable", false) })
	sm("immutable absent", func(o m) { drop(o, "immutable") })
	sm("type dockerconfigjson", func(o m) { put(o, "type", "kubernetes.io/dockerconfigjson") })
	sm("type absent", func(o m) { drop(o, "type") })
	sm("extra data key", func(o m) { put(o, "data/extra", "eA==") })
	sm("other data key", func(o m) { put(o, "data", m{"password": "eA=="}) })
	sm("no data", func(o m) { drop(o, "data") })
	sm("stringData", func(o m) { put(o, "stringData", m{"x": "y"}) })
	sm("name absent (generateName)", func(o m) { drop(o, "metadata/name") })
	b.add("deny Secret: other namespace", "CREATE", "", "secrets", func() m { s := secret("researcher"); put(s, "metadata/namespace", "kube-system"); return s }(), nil, deny)
	b.add("deny Secret: UPDATE", "UPDATE", "", "secrets", secret("researcher"), secret("researcher"), deny)
	{
		d := secret("researcher")
		put(d, "metadata/name", "kagent-postgresql")
		b.add("deny Secret DELETE: foreign name with both labels", "DELETE", "", "secrets", nil, d, deny)
		d = secret("researcher")
		delLabel(d, lblManaged)
		b.add("deny Secret DELETE: right name, no managed-by label", "DELETE", "", "secrets", nil, d, deny)
		d = secret("researcher")
		drop(d, "metadata/labels")
		b.add("deny Secret DELETE: right name, no labels", "DELETE", "", "secrets", nil, d, deny)
		d = secret("researcher")
		delLabel(d, lblDef)
		b.add("deny Secret DELETE: right name, no agent-definition label", "DELETE", "", "secrets", nil, d, deny)
		d = secret("researcher")
		setLabel(d, lblDef, "someone-else")
		b.add("deny Secret DELETE: label names another agent", "DELETE", "", "secrets", nil, d, deny)
		d = secret("researcher")
		put(d, "metadata/name", "anthropic-api-key")
		b.add("deny Secret DELETE: another name, both labels", "DELETE", "", "secrets", nil, d, deny)
		b.add("deny Secret DELETE: other namespace", "DELETE", "", "secrets", nil, func() m { s := secret("researcher"); put(s, "metadata/namespace", "kube-system"); return s }(), deny)
	}

	// ---- denied: ConfigMaps
	cmOK := m{"kagent.researcher": entry("kagent.researcher", hash64, "", "")}
	cm := func(name string, data m, f func(o m)) {
		o := keyMap(data)
		if f != nil {
			f(o)
		}
		b.add("deny ConfigMap: "+name, "UPDATE", "", "configmaps", o, keyMap(cmOK), deny)
	}
	cm("another gateway user id (a foreign prefix)", m{"other.researcher": entry("other.researcher", hash64, "", "")}, nil)
	cm("a user id without the prefix, newly added", m{"admin": entry("admin", hash64, "", "")}, nil)
	cm("a chart agent's plain name, newly added", m{"assistant": entry("assistant", hash64, "", "")}, nil)
	cm("an upper case name", m{"kagent.Researcher": entry("kagent.Researcher", hash64, "", "")}, nil)
	cm("a name with a second dot", m{"kagent.a.b": entry("kagent.a.b", hash64, "", "")}, nil)
	cm("user_id differs from the key", m{"kagent.researcher": entry("kagent.admin", hash64, "", "")}, nil)
	cm("keyHash too short", m{"kagent.researcher": entry("kagent.researcher", "sha256:abcd", "", "")}, nil)
	cm("keyHash upper case", m{"kagent.researcher": entry("kagent.researcher", "sha256:"+strings.Repeat("AB12", 16), "", "")}, nil)
	cm("keyHash of another algorithm", m{"kagent.researcher": entry("kagent.researcher", "sha1:"+strings.Repeat("ab12", 16), "", "")}, nil)
	cm("keyHash empty", m{"kagent.researcher": entry("kagent.researcher", "", "", "")}, nil)
	cm("an extra field in the entry", m{"kagent.researcher": strings.Replace(entry("kagent.researcher", hash64, "", ""), `"metadata"`, `"role":"admin","metadata"`, 1)}, nil)
	cm("an extra field in the metadata", m{"kagent.researcher": strings.Replace(entry("kagent.researcher", hash64, "", ""), `"user_id"`, `"admin":true,"user_id"`, 1)}, nil)
	cm("a team with a quote", m{"kagent.researcher": strings.Replace(entry("kagent.researcher", hash64, "x", ""), `"team":"x"`, `"team":"x\",\"admin\":\"1"`, 1)}, nil)
	cm("whitespace in the entry", m{"kagent.researcher": " " + entry("kagent.researcher", hash64, "", "")}, nil)
	cm("not JSON", m{"kagent.researcher": "sha256:abc"}, nil)
	cm("binaryData", cmOK, func(o m) { o["binaryData"] = m{"x": "eA=="} })
	cm("immutable", cmOK, func(o m) { o["immutable"] = true })
	cm("the label the gateway policy selects on is removed", cmOK, func(o m) { delLabel(o, "agentgateway.dev/apikey") })
	cm("a label is added", cmOK, func(o m) { setLabel(o, "x", "y") })
	cm("an annotation is added", cmOK, func(o m) { o["metadata"].(m)["annotations"] = m{"x": "y"} })
	cm("an owner reference is added", cmOK, func(o m) { o["metadata"].(m)["ownerReferences"] = []any{owner("researcher")} })
	{
		legacyOld := m{"researcher": `{"keyHash":"sha256:` + strings.Repeat("0", 64) + `","metadata":{"user_id":"researcher"}}`}
		o := keyMap(m{"researcher": entry("researcher", hash64, "", "")})
		b.add("deny ConfigMap: an entry of the previous format is rewritten (only kept)", "UPDATE", "", "configmaps", o, keyMap(legacyOld), deny)
	}
	other := m{"apiVersion": "v1", "kind": "ConfigMap", "metadata": m{"name": "llm-api-keys", "namespace": "agentgateway-system"}, "data": m{}}
	b.add("deny ConfigMap: the installer's key ConfigMap", "UPDATE", "", "configmaps", other, deepCopy(other), deny)
	b.add("deny ConfigMap: another namespace", "UPDATE", "", "configmaps", keyMap(cmOK), keyMap(cmOK), deny)
	b.out[len(b.out)-1].req.Namespace = "kagent"
	b.add("deny ConfigMap: CREATE", "CREATE", "", "configmaps", keyMap(cmOK), nil, deny)
	b.add("deny ConfigMap: DELETE", "DELETE", "", "configmaps", nil, keyMap(cmOK), deny)

	// ---- denied: the five kagent kinds, generic
	for _, k := range kinds {
		base := find(A, k)
		mut := func(name string, f func(o m)) {
			o := deepCopy(base)
			f(o)
			b.kagent("deny "+k+": "+name, "CREATE", k, o, nil, deny)
		}
		mut("no labels", func(o m) { drop(o, "metadata/labels") })
		mut("no managed-by", func(o m) { delLabel(o, lblManaged) })
		mut("no agent-definition", func(o m) { delLabel(o, lblDef) })
		mut("no ownerReferences", func(o m) { drop(o, "metadata/ownerReferences") })
		mut("ownerRef controller false", func(o m) { put(o, "metadata/ownerReferences/0/controller", false) })
		mut("ownerRef kind Agent", func(o m) { put(o, "metadata/ownerReferences/0/kind", "Agent") })
		mut("ownerRef name differs from the label", func(o m) { put(o, "metadata/ownerReferences/0/name", "victim") })
		mut("Helm labels only (a chart agent's object)", func(o m) {
			o["metadata"].(m)["labels"] = m{lblManaged: "Helm"}
			drop(o, "metadata/ownerReferences")
		})
		mut("other namespace", func(o m) { put(o, "metadata/namespace", "kube-system") })
		mut("an unknown label", func(o m) { setLabel(o, "x", "y") })
		mut("an unknown annotation", func(o m) { o["metadata"].(m)["annotations"] = m{"x": "y"} })
		mut("generateName instead of name", func(o m) { o["metadata"].(m)["generateName"] = "x-" })
		mut("an unknown metadata field", func(o m) { o["metadata"].(m)["clusterName"] = "x" })
		mut("an unknown top-level field", func(o m) { o["extra"] = m{"x": 1} })
		mut("an unknown spec field", func(o m) { put(o, "spec/surprise", "x") })
		mut("no spec", func(o m) { drop(o, "spec") })
		mut("the spec is empty", func(o m) { o["spec"] = m{} })
		foreign := func(x m) m {
			md := x["metadata"].(m)
			delete(md, "ownerReferences")
			md["labels"] = m{lblManaged: "Helm"}
			return x
		}
		b.kagent("deny "+k+": UPDATE of an object that is not owned (takeover)", "UPDATE", k, deepCopy(base), foreign(deepCopy(base)), deny)
		b.kagent("deny "+k+": DELETE of an object that is not owned", "DELETE", k, nil, foreign(deepCopy(base)), deny)
		nc := deepCopy(base)
		put(nc, "metadata/ownerReferences/0/controller", false)
		b.kagent("deny "+k+": DELETE of an object whose owner reference is not a controller", "DELETE", k, nil, nc, deny)
	}

	// ---- C1: EgressPolicy authentication (the egress gateway would send a Secret's value to the origin)
	{
		e := find(A, "EgressPolicy")
		put(e, "spec/destinations/0/authentication", m{"secretKeyRef": m{"name": "default-model-config", "key": "token"}, "header": "Authorization"})
		b.kagent("deny EgressPolicy: authentication.secretKeyRef (a Secret's value sent to the origin)", "CREATE", "EgressPolicy", e, nil, deny)
		e = find(A, "EgressPolicy")
		put(e, "spec/destinations/0/authentication", m{"secretKeyRef": m{"name": "kagent-postgresql", "key": "password"}, "header": "X-Leak", "prefix": ""})
		b.kagent("deny EgressPolicy: authentication.secretKeyRef on UPDATE", "UPDATE", "EgressPolicy", e, find(A, "EgressPolicy"), deny)
		e = find(A, "EgressPolicy")
		put(e, "spec/destinations/0/authentication", m{"actorJwt": m{"expirationSeconds": 900}})
		b.kagent("deny EgressPolicy: authentication.actorJwt", "CREATE", "EgressPolicy", e, nil, deny)
		e = find(A, "EgressPolicy")
		put(e, "spec/destinations/0/authentication", m{})
		b.kagent("deny EgressPolicy: an empty authentication", "CREATE", "EgressPolicy", e, nil, deny)
		e = find(A, "EgressPolicy")
		put(e, "spec/destinations/0/headers", m{"x": "y"})
		b.kagent("deny EgressPolicy: any other key of a destination", "CREATE", "EgressPolicy", e, nil, deny)
		e = find(A, "EgressPolicy")
		put(e, "spec/destinations", []any{m{"origin": "https://api.osv.dev"}, m{"origin": "https://evil.example.com", "authentication": m{"secretKeyRef": m{"name": "s", "key": "k"}}}})
		b.kagent("deny EgressPolicy: authentication on the second destination", "CREATE", "EgressPolicy", e, nil, deny)
		e = find(A, "EgressPolicy")
		put(e, "spec/extra", "x")
		b.kagent("deny EgressPolicy: an unknown spec key", "CREATE", "EgressPolicy", e, nil, deny)
		e = find(A, "EgressPolicy")
		put(e, "metadata/name", "victim-egress")
		b.kagent("deny EgressPolicy: another agent's name", "CREATE", "EgressPolicy", e, nil, deny)
		e = find(A, "EgressPolicy")
		drop(e, "spec/destinations")
		b.kagent("deny EgressPolicy: no destinations", "CREATE", "EgressPolicy", e, nil, deny)
		e = find(A, "EgressPolicy")
		put(e, "spec/destinations", []any{m{"origin": 5}})
		b.kagent("deny EgressPolicy: an origin that is not a string", "CREATE", "EgressPolicy", e, nil, deny)
	}

	// ---- I1: Harness
	{
		h := func(name string, f func(o m)) {
			o := find(A, "Harness")
			f(o)
			b.kagent("deny Harness: "+name, "CREATE", "Harness", o, nil, deny)
		}
		h("workload.command", func(o m) { put(o, "spec/workload/command", []any{"/bin/sh", "-c", "id"}) })
		h("workload.args", func(o m) { put(o, "spec/workload/args", []any{"--debug"}) })
		h("spec.byo", func(o m) { put(o, "spec/byo", m{"image": "evil.example/x"}) })
		h("spec.claude", func(o m) { put(o, "spec/claude", m{}) })
		h("spec.codex", func(o m) { put(o, "spec/codex", m{}) })
		h("kagent.compaction.summarizer.modelConfigRef", func(o m) {
			put(o, "spec/kagent/compaction", m{"summarizer": m{"modelConfigRef": m{"name": "default-model-config", "namespace": "kagent"}}})
		})
		h("kagent.compaction", func(o m) { put(o, "spec/kagent/compaction", m{"tokenThreshold": 100}) })
		h("an unknown kagent key", func(o m) { put(o, "spec/kagent/surprise", true) })
		h("an unknown workload key", func(o m) { put(o, "spec/workload/securityContext", m{"privileged": true}) })
		h("an unknown substrate key", func(o m) { put(o, "spec/substrate/surprise", "x") })
		h("an unknown snapshotPolicy key", func(o m) { put(o, "spec/substrate/snapshotPolicy/bucket", "x") })
		h("an unknown workerPoolRef key", func(o m) { put(o, "spec/substrate/workerPoolRef/namespace", "x") })
		h("other image", func(o m) { put(o, "spec/workload/image", "evil.example/img:latest") })
		h("image absent", func(o m) { drop(o, "spec/workload") })
		h("other worker pool", func(o m) { put(o, "spec/substrate/workerPoolRef/name", "privileged") })
		h("other snapshot location", func(o m) { put(o, "spec/substrate/snapshotPolicy/location", "s3://evil/") })
		h("substrate absent", func(o m) { drop(o, "spec/substrate") })
		h("env entry added", func(o m) { put(o, "spec/env", []any{m{"name": "X", "value": "y"}}) })
		h("env from a secret", func(o m) {
			put(o, "spec/env", []any{m{"name": "X", "valueFrom": m{"secretKeyRef": m{"name": "s", "key": "k"}}}})
		})
		h("memory on another ModelConfig", func(o m) {
			put(o, "spec/kagent/memory", m{"modelConfigRef": m{"name": "gateway-victim", "namespace": "kagent"}})
		})
		h("memory ModelConfig in another namespace", func(o m) {
			put(o, "spec/kagent/memory", m{"modelConfigRef": m{"name": "gateway-embed-memory", "namespace": "default"}})
		})
		h("memory with an unknown key", func(o m) {
			put(o, "spec/kagent/memory", m{"modelConfigRef": m{"name": "gateway-embed-memory", "namespace": "kagent"}, "surprise": 1})
		})
		h("memory ttlDays above the policy maximum", func(o m) {
			put(o, "spec/kagent/memory", m{"modelConfigRef": m{"name": "gateway-embed-memory", "namespace": "kagent"}, "ttlDays": 3650})
		})
		h("wrong name", func(o m) { put(o, "metadata/name", "victim-harness") })
	}
	// ---- I1: ModelConfig
	{
		mcf := func(name string, f func(o m)) {
			o := find(A, "ModelConfig")
			f(o)
			b.kagent("deny ModelConfig: "+name, "CREATE", "ModelConfig", o, nil, deny)
		}
		mcf("a model that is not in the catalog", func(o m) { put(o, "spec/model", "gpt-4o-not-in-the-catalog") })
		mcf("model absent", func(o m) { drop(o, "spec/model") })
		mcf("defaultHeaders", func(o m) { put(o, "spec/defaultHeaders", m{"x-leak": "y"}) })
		mcf("tls.disableVerify", func(o m) { put(o, "spec/tls", m{"disableVerify": true}) })
		mcf("tls with any content", func(o m) { put(o, "spec/tls", m{}) })
		mcf("actorJwt", func(o m) { put(o, "spec/actorJwt", m{"expirationSeconds": 900}) })
		mcf("tokenPassthrough", func(o m) { put(o, "spec/tokenPassthrough", true) })
		mcf("allowedNamespaces", func(o m) { put(o, "spec/allowedNamespaces", m{"from": "All"}) })
		mcf("another provider block", func(o m) { put(o, "spec/anthropic", m{"baseUrl": "https://evil.example.com"}) })
		mcf("an unknown spec key", func(o m) { put(o, "spec/surprise", 1) })
		mcf("an unknown openAI key", func(o m) { put(o, "spec/openAI/organization", "x") })
		mcf("openAI.apiFormat other than the default", func(o m) { put(o, "spec/openAI/apiFormat", "responses") })
		mcf("provider Anthropic", func(o m) { put(o, "spec/provider", "Anthropic") })
		mcf("other base URL", func(o m) { put(o, "spec/openAI/baseUrl", "https://api.openai.com/v1") })
		mcf("no openAI block", func(o m) { drop(o, "spec/openAI") })
		mcf("stream true", func(o m) { put(o, "spec/stream", true) })
		mcf("key Secret of another agent", func(o m) { put(o, "spec/apiKeySecret", "victim-virtual-key-1a2b3c4d") })
		mcf("key Secret without hex", func(o m) { put(o, "spec/apiKeySecret", "researcher-virtual-key") })
		mcf("key Secret upper case hex", func(o m) { put(o, "spec/apiKeySecret", "researcher-virtual-key-1A2B3C4D") })
		mcf("key Secret with 9 hex", func(o m) { put(o, "spec/apiKeySecret", "researcher-virtual-key-1a2b3c4d5") })
		mcf("the provider key Secret", func(o m) { put(o, "spec/apiKeySecret", "anthropic-api-key") })
		mcf("other secret key", func(o m) { put(o, "spec/apiKeySecretKey", "ANTHROPIC_API_KEY") })
		mcf("name of another agent", func(o m) { put(o, "metadata/name", "gateway-victim") })
		mcf("name default-model-config", func(o m) { put(o, "metadata/name", "default-model-config") })
		mcf("apiKeySecret absent", func(o m) { drop(o, "spec/apiKeySecret") })
	}
	// ---- I1: AgentTemplate
	{
		tf := func(name string, f func(o m)) {
			o := find(A, "AgentTemplate")
			f(o)
			b.kagent("deny AgentTemplate: "+name, "CREATE", "AgentTemplate", o, nil, deny)
		}
		server0 := "spec/tools/0/mcp/server"
		tf("plugins", func(o m) {
			put(o, "spec/plugins", []any{m{"skills": []any{m{"name": "x", "source": m{"oci": "evil.example/p@sha256:" + strings.Repeat("0", 64)}}}}})
		})
		tf("promptTemplate.dataSources", func(o m) {
			put(o, "spec/promptTemplate", m{"dataSources": []any{m{"alias": "x", "name": "kagent-postgresql"}}})
		})
		tf("systemPromptFrom", func(o m) { put(o, "spec/systemPromptFrom", m{"name": "kagent-postgresql", "key": "password"}) })
		tf("outputSchemaFrom", func(o m) { put(o, "spec/outputSchemaFrom", m{"name": "kagent-postgresql", "key": "password"}) })
		tf("outputSchema", func(o m) { put(o, "spec/outputSchema", m{"type": "object"}) })
		tf("outputSchemaStrict", func(o m) { put(o, "spec/outputSchemaStrict", true) })
		tf("modelConfig.namespace", func(o m) { put(o, "spec/modelConfig/namespace", "default") })
		tf("tools[].mcp.server.namespace", func(o m) { put(o, server0+"/namespace", "kube-system") })
		tf("tools[].mcp.server.apiGroup", func(o m) { put(o, server0+"/apiGroup", "kagent.dev") })
		tf("tools[].mcp.requireApproval", func(o m) { put(o, "spec/tools/0/mcp/requireApproval", false) })
		tf("tools[].mcp with an unknown key", func(o m) { put(o, "spec/tools/0/mcp/surprise", 1) })
		tf("tools[].mcp.server with an unknown key", func(o m) { put(o, server0+"/surprise", 1) })
		tf("tools[].mcp.tools a tool the profile does not offer", func(o m) { put(o, "spec/tools/0/mcp/tools", []any{prof.Tools[0], "k8s_delete_resource"}) })
		tf("tools[].mcp.tools another server's tool", func(o m) { put(o, "spec/tools/0/mcp/tools", []any{"k8s_get_resources"}) })
		tf("tools[].mcp.tools absent (every tool of the server)", func(o m) { drop(o, "spec/tools/0/mcp/tools") })
		tf("tools[].mcp.tools not a list", func(o m) { put(o, "spec/tools/0/mcp/tools", "all") })
		tf("tools[].subAgent", func(o m) {
			o["spec"].(m)["tools"] = append(o["spec"].(m)["tools"].([]any), m{"subAgent": m{"name": "victim"}})
		})
		tf("tool server kagent-tool-server", func(o m) { put(o, server0+"/name", "kagent-tool-server") })
		tf("shared vMCP server", func(o m) { put(o, server0+"/name", "toolhive-vmcp") })
		tf("server kind Agent", func(o m) { put(o, server0+"/kind", "Agent") })
		tf("server kind missing", func(o m) { drop(o, server0+"/kind") })
		tf("server name missing", func(o m) { drop(o, server0+"/name") })
		tf("tool is an agent, no mcp", func(o m) {
			o["spec"].(m)["tools"] = append(o["spec"].(m)["tools"].([]any), m{"agent": m{"name": "victim"}})
		})
		tf("a second tool entry on a foreign server", func(o m) {
			o["spec"].(m)["tools"] = append(o["spec"].(m)["tools"].([]any), m{"mcp": m{"server": m{"kind": "RemoteMCPServer", "name": "kagent-tool-server"}, "tools": []any{"k8s_get_resources"}}})
		})
		tf("skill from another registry", func(o m) {
			put(o, "spec/skills", []any{m{"name": "x", "source": m{"oci": "evil.example/skill@sha256:" + strings.Repeat("0", 64)}}})
		})
		tf("skill from git", func(o m) { put(o, "spec/skills", []any{m{"name": "x", "source": m{"git": m{"url": "https://x"}}}}) })
		tf("skill with an unknown key", func(o m) {
			put(o, "spec/skills", []any{m{"name": "x", "source": m{"oci": firstSkill(cat)}, "path": "/"}})
		})
		tf("other modelConfig", func(o m) { put(o, "spec/modelConfig/name", "default-model-config") })
		tf("modelConfig of another agent", func(o m) { put(o, "spec/modelConfig/name", "gateway-victim") })
		tf("modelConfig absent", func(o m) { drop(o, "spec/modelConfig") })
		tf("an unknown spec key", func(o m) { put(o, "spec/surprise", 1) })
		tf("wrong name", func(o m) { put(o, "metadata/name", "victim-template") })
		tf("no spec", func(o m) { drop(o, "spec") })
	}
	// ---- I1: Agent
	{
		af := func(name string, f func(o m)) {
			o := find(A, "Agent")
			f(o)
			b.kagent("deny Agent: "+name, "CREATE", "Agent", o, nil, deny)
		}
		af("template of another agent", func(o m) { put(o, "spec/template/ref/name", "victim-template") })
		af("an inline template", func(o m) { put(o, "spec/template/inline", m{"systemPrompt": "x"}) })
		af("a template ref with a namespace", func(o m) { put(o, "spec/template/ref/namespace", "default") })
		af("harness of another agent", func(o m) { put(o, "spec/harnessRef/name", "victim-harness") })
		af("a harness ref with a namespace", func(o m) { put(o, "spec/harnessRef/namespace", "default") })
		af("egress of another agent", func(o m) { put(o, "spec/egressPolicyRefs", []any{m{"name": "victim-egress"}}) })
		af("two egress refs", func(o m) {
			put(o, "spec/egressPolicyRefs", []any{m{"name": "researcher-egress"}, m{"name": "victim-egress"}})
		})
		af("an egress ref with a namespace", func(o m) { put(o, "spec/egressPolicyRefs", []any{m{"name": "researcher-egress", "namespace": "x"}}) })
		af("template ref absent", func(o m) { drop(o, "spec/template") })
		af("harness ref absent", func(o m) { drop(o, "spec/harnessRef") })
		af("an unknown spec key", func(o m) { put(o, "spec/surprise", 1) })
		af("wrong name", func(o m) { put(o, "metadata/name", "victim") })
	}

	// ---- the last rule: anything else reaching the guard is refused
	for _, c := range []struct{ group, res, op string }{
		{"", "pods", "CREATE"}, {"", "pods", "DELETE"}, {"", "serviceaccounts", "CREATE"}, {"", "namespaces", "DELETE"},
		{"rbac.authorization.k8s.io", "rolebindings", "CREATE"}, {"rbac.authorization.k8s.io", "clusterroles", "UPDATE"},
		{"apps", "deployments", "UPDATE"}, {"openagentix.io", "agentdefinitions", "DELETE"}, {"openagentix.io", "agentdefinitions", "CREATE"},
		{"api.kagent.dev", "remotemcpservers", "CREATE"}, {"api.kagent.dev", "modelproviderconfigs", "UPDATE"}, {"api.kagent.dev", "sandboxtemplates", "CREATE"},
		{"admissionregistration.k8s.io", "validatingadmissionpolicies", "DELETE"}, {"networking.k8s.io", "networkpolicies", "DELETE"},
	} {
		o := m{"metadata": m{"name": "x"}}
		var obj, old m
		switch c.op {
		case "CREATE":
			obj = o
		case "UPDATE":
			obj, old = o, deepCopy(o)
		default:
			old = o
		}
		b.out = append(b.out, expectation{req: request{Name: fmt.Sprintf("deny any other write: %s %s/%s", c.op, c.group, c.res), User: controller, Op: c.op, Group: c.group, Resource: c.res, Namespace: "kagent", ObjName: "x", Object: obj, Old: old}, allow: false})
	}
	b.out = append(b.out,
		expectation{req: request{Name: "deny any other write: a subresource (pods/exec)", User: controller, Op: "CREATE", Resource: "pods", Sub: "exec", Namespace: "kagent", ObjName: "x", Object: m{"metadata": m{"name": "x"}}}, allow: false},
		expectation{req: request{Name: "deny any other write: a definition's scale subresource", User: controller, Op: "UPDATE", Group: "openagentix.io", Resource: "agentdefinitions", Sub: "scale", Namespace: "kagent", ObjName: "x", Object: m{"metadata": m{"name": "x"}}, Old: m{"metadata": m{"name": "x"}}}, allow: false},
		expectation{req: request{Name: "deny any other write: a kagent object's status subresource", User: controller, Op: "UPDATE", Group: "api.kagent.dev", Resource: "agents", Sub: "status", Namespace: "kagent", ObjName: "x", Object: m{"metadata": m{"name": "x"}}, Old: m{"metadata": m{"name": "x"}}}, allow: false},
	)
	return b.out
}

func strs(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

func firstSkill(c catalogFile) string {
	for _, v := range c.Skills {
		return v
	}
	return "registry.invalid:5000/skills/x@sha256:" + strings.Repeat("0", 64)
}
