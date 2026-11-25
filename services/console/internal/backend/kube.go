package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/openagentix/openagentix/services/console/internal/kube"
	"github.com/openagentix/openagentix/services/console/internal/model"
	"github.com/openagentix/openagentix/services/console/internal/platform"
)

// FieldManager is the name the API server records for writes made by the console.
const FieldManager = "openagentix-console"

// Kube reads and writes through the Kubernetes API. The paths it can call are the ones built in this file, all inside
// one namespace: AgentDefinitions (read and write), and the kagent Agent, AgentTemplate, Harness and ModelConfig (read only).
type Kube struct {
	C            *kube.Client
	Namespace    string
	KagentAPI    string // for example api.kagent.dev/v1alpha3
	Platform     platform.Source
	defsPath     string
	kagentPrefix string
}

func NewKube(c *kube.Client, ns, kagentAPI string, p platform.Source) *Kube {
	return &Kube{C: c, Namespace: ns, KagentAPI: kagentAPI, Platform: p,
		defsPath: "/apis/" + model.Group + "/" + model.Version + "/namespaces/" + url.PathEscape(ns) + "/agentdefinitions"}
}

func (k *Kube) kagent(plural string) string {
	return "/apis/" + k.KagentAPI + "/namespaces/" + url.PathEscape(k.Namespace) + "/" + plural
}

type statusObj struct {
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Details struct {
		Causes []struct {
			Message string `json:"message"`
			Field   string `json:"field"`
		} `json:"causes"`
	} `json:"details"`
}

// fail turns an unexpected API server answer into the sentence the person sees (what happened, why, what to do).
func (k *Kube) fail(what string, r kube.Response) *Error {
	var st statusObj
	_ = json.Unmarshal(r.Body, &st)
	switch r.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return Errf(http.StatusBadGateway, "cluster_forbidden",
			"Could not %s: the Kubernetes API returned %d. The console's ServiceAccount may not do this in namespace %s. Ask an operator to bind the openagentix-agent-builder Role.", what, r.Status, k.Namespace)
	case http.StatusNotFound:
		return Errf(http.StatusBadGateway, "cluster_missing",
			"Could not %s: the Kubernetes API returned 404. The AgentDefinition kind may not be installed. Check that controlPlane.enabled is true in chart values.", what)
	}
	return Errf(http.StatusBadGateway, "cluster_error", "Could not %s: the Kubernetes API returned %d. Check that the API server is reachable from the console pod.", what, r.Status)
}

func (k *Kube) call(ctx context.Context, what, method, path string, q url.Values, body []byte) (kube.Response, error) {
	ct := ""
	if body != nil {
		ct = "application/json"
	}
	r, err := k.C.Do(ctx, method, path, q, ct, body)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return r, Errf(http.StatusGatewayTimeout, "timeout", "Could not %s: the Kubernetes API did not answer in time. Try again.", what)
		}
		if errors.Is(err, context.Canceled) {
			return r, err
		}
		return r, Errf(http.StatusBadGateway, "cluster_unreachable", "Could not %s: the Kubernetes API is not reachable from the console pod. Check the cluster and the network.", what)
	}
	return r, nil
}

type list[T any] struct {
	Items []T `json:"items"`
}

func (k *Kube) defs(ctx context.Context) ([]Definition, error) {
	r, err := k.call(ctx, "list agent definitions", http.MethodGet, k.defsPath, nil, nil)
	if err != nil {
		return nil, err
	}
	if r.Status != http.StatusOK {
		return nil, k.fail("list agent definitions", r)
	}
	var l list[Definition]
	if err := json.Unmarshal(r.Body, &l); err != nil {
		return nil, Errf(http.StatusBadGateway, "cluster_error", "Could not list agent definitions: the Kubernetes API sent something that is not JSON.")
	}
	return l.Items, nil
}

func (k *Kube) def(ctx context.Context, name string) (Definition, *Error, error) {
	r, err := k.call(ctx, "read "+name, http.MethodGet, k.defsPath+"/"+url.PathEscape(name), nil, nil)
	if err != nil {
		return Definition{}, nil, err
	}
	switch r.Status {
	case http.StatusOK:
		var d Definition
		if err := json.Unmarshal(r.Body, &d); err != nil {
			return d, nil, Errf(http.StatusBadGateway, "cluster_error", "Could not read %s: the Kubernetes API sent something that is not JSON.", name)
		}
		return d, nil, nil
	case http.StatusNotFound:
		return Definition{}, Errf(http.StatusNotFound, "not_found", "There is no built agent named %s in namespace %s.", name, k.Namespace), nil
	}
	return Definition{}, nil, k.fail("read "+name, r)
}

// --- chart agents ---

type rawObj struct {
	Metadata Meta            `json:"metadata"`
	Spec     json.RawMessage `json:"spec"`
	Status   struct {
		Conditions []model.Condition `json:"conditions"`
	} `json:"status"`
}

func (k *Kube) chartObjects(ctx context.Context, plural string, selector string) ([]rawObj, error) {
	q := url.Values{}
	if selector != "" {
		q.Set("labelSelector", selector)
	}
	r, err := k.call(ctx, "list "+plural, http.MethodGet, k.kagent(plural), q, nil)
	if err != nil {
		return nil, err
	}
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("%s: the Kubernetes API returned %d", plural, r.Status)
	}
	var l list[rawObj]
	if err := json.Unmarshal(r.Body, &l); err != nil {
		return nil, fmt.Errorf("%s: not JSON", plural)
	}
	return l.Items, nil
}

// charts lists the kagent Agents that carry no openagentix.io/agent-definition label. Reading their template, harness and
// model config is best effort: a refusal becomes a warning and the row has fewer details.
func (k *Kube) charts(ctx context.Context) ([]model.Agent, map[string]*model.Spec, []string, error) {
	agents, err := k.chartObjects(ctx, "agents", "!"+model.LabelDef)
	if err != nil {
		var ae *Error
		if errors.As(err, &ae) || errors.Is(err, context.Canceled) {
			return nil, nil, nil, err
		}
		return nil, nil, []string{"Chart agents could not be listed (" + err.Error() + "). Only built agents are shown. The console's ServiceAccount needs read access to agents.api.kagent.dev."}, nil
	}
	var warn []string
	extra := func(plural string) map[string]rawObj {
		m := map[string]rawObj{}
		items, err := k.chartObjects(ctx, plural, "")
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return m
			}
			warn = append(warn, "Details of chart agents are partly missing: "+err.Error()+".")
			return m
		}
		for _, o := range items {
			m[o.Metadata.Name] = o
		}
		return m
	}
	templates, harnesses, mcs := extra("agenttemplates"), extra("harnesses"), extra("modelconfigs")

	profiles, knowledge, notify := map[string]string{}, []string(nil), []string(nil)
	if b, err := k.Platform.Get(); err == nil {
		for _, p := range b.Catalog.Profiles {
			profiles[p.ToolServer] = p.Name
		}
		knowledge, notify = b.Catalog.Knowledge.Tools, b.Catalog.Notify.Tools
	}
	var out []model.Agent
	specs := map[string]*model.Spec{}
	for _, a := range agents {
		c := Chart{Name: a.Metadata.Name, Namespace: a.Metadata.Namespace, Created: a.Metadata.CreationTimestamp, Conditions: a.Status.Conditions}
		var as struct {
			Template struct {
				Ref struct{ Name string } `json:"ref"`
			} `json:"template"`
			HarnessRef struct{ Name string } `json:"harnessRef"`
		}
		_ = json.Unmarshal(a.Spec, &as)
		if t, ok := templates[as.Template.Ref.Name]; ok {
			var ts struct {
				Description  string                `json:"description"`
				SystemPrompt string                `json:"systemPrompt"`
				ModelConfig  struct{ Name string } `json:"modelConfig"`
				Tools        []struct {
					MCP struct {
						Server struct{ Name string } `json:"server"`
						Tools  []string              `json:"tools"`
					} `json:"mcp"`
				} `json:"tools"`
				Skills []struct{ Name string } `json:"skills"`
			}
			_ = json.Unmarshal(t.Spec, &ts)
			c.Description, c.Prompt, c.ModelConfigName = ts.Description, ts.SystemPrompt, ts.ModelConfig.Name
			for _, tl := range ts.Tools {
				c.ToolServer = tl.MCP.Server.Name
				c.Tools = append(c.Tools, tl.MCP.Tools...)
			}
			for _, s := range ts.Skills {
				c.Skills = append(c.Skills, s.Name)
			}
		}
		if mc, ok := mcs[c.ModelConfigName]; ok {
			var ms struct{ Model string }
			_ = json.Unmarshal(mc.Spec, &ms)
			c.Model = ms.Model
		}
		if h, ok := harnesses[as.HarnessRef.Name]; ok {
			var hs struct {
				Kagent struct {
					Memory *struct {
						TTLDays int `json:"ttlDays"`
					} `json:"memory"`
				} `json:"kagent"`
			}
			_ = json.Unmarshal(h.Spec, &hs)
			if hs.Kagent.Memory != nil {
				c.HasMemory, c.MemoryTTL = true, hs.Kagent.Memory.TTLDays
			}
		}
		row, spec := ChartAgent(c, profiles, knowledge, notify)
		out = append(out, row)
		specs[row.Name] = spec
	}
	return out, specs, warn, nil
}

// --- Backend ---

func (k *Kube) List(ctx context.Context) (model.AgentList, error) {
	defs, err := k.defs(ctx)
	if err != nil {
		return model.AgentList{}, err
	}
	res := model.AgentList{Items: []model.Agent{}, Warnings: []string{}}
	for _, d := range defs {
		res.Items = append(res.Items, BuiltAgent(d))
		res.Used++
	}
	chart, _, warn, err := k.charts(ctx)
	if err != nil {
		return model.AgentList{}, err
	}
	res.Items = append(res.Items, chart...)
	res.Warnings = append(res.Warnings, warn...)
	sort.Slice(res.Items, func(i, j int) bool { return res.Items[i].Name < res.Items[j].Name })
	return res, nil
}

func (k *Kube) Get(ctx context.Context, name string) (model.Detail, error) {
	d, ke, err := k.def(ctx, name)
	if err != nil {
		return model.Detail{}, err
	}
	if ke == nil {
		return BuiltDetail(d), nil
	}
	chart, specs, _, err := k.charts(ctx)
	if err != nil {
		return model.Detail{}, err
	}
	for _, a := range chart {
		if a.Name == name {
			return model.Detail{Agent: a, ReadOnly: true, Spec: specs[name], Objects: []model.OwnedObject{}}, nil
		}
	}
	return model.Detail{}, Errf(http.StatusNotFound, "not_found", "There is no agent named %s in namespace %s.", name, k.Namespace)
}

func (k *Kube) object(name string, spec model.Spec, rv string) ([]byte, error) {
	meta := map[string]any{"name": name, "namespace": k.Namespace}
	if rv != "" {
		meta["resourceVersion"] = rv
	}
	return json.Marshal(map[string]any{"apiVersion": model.APIVersion, "kind": model.Kind, "metadata": meta, "spec": spec})
}

// writeErr maps the API server's answer to a write.
func (k *Kube) writeErr(what, name string, r kube.Response) *Error {
	var st statusObj
	_ = json.Unmarshal(r.Body, &st)
	switch r.Status {
	case http.StatusConflict:
		if st.Reason == "AlreadyExists" {
			return Errf(http.StatusConflict, "exists", "An agent named %s already exists. Pick another name.", name)
		}
		return Errf(http.StatusConflict, "conflict", "%s was changed by someone else (or by the controller) since you opened it. Reload it, then apply your changes again.", name)
	case http.StatusNotFound:
		return Errf(http.StatusNotFound, "not_found", "There is no built agent named %s in namespace %s.", name, k.Namespace)
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		var msgs []string
		for _, c := range st.Details.Causes {
			m := c.Message
			if c.Field != "" {
				m = c.Field + ": " + m
			}
			msgs = append(msgs, m)
		}
		if len(msgs) == 0 && st.Message != "" {
			msgs = []string{st.Message}
		}
		return Errf(http.StatusUnprocessableEntity, "invalid", "The Kubernetes API refused %s as invalid: %s", name, strings.Join(msgs, "; "))
	}
	return k.fail(what, r)
}

func (k *Kube) takenByChart(ctx context.Context, name string) (bool, error) {
	chart, _, _, err := k.charts(ctx)
	if err != nil {
		return false, err
	}
	for _, a := range chart {
		if a.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (k *Kube) Create(ctx context.Context, name string, spec model.Spec) (model.Detail, error) {
	if taken, err := k.takenByChart(ctx, name); err != nil {
		return model.Detail{}, err
	} else if taken {
		return model.Detail{}, Errf(http.StatusConflict, "exists", "An agent named %s already exists: it comes from chart values. Pick another name.", name)
	}
	body, _ := k.object(name, spec, "")
	r, err := k.call(ctx, "create "+name, http.MethodPost, k.defsPath, url.Values{"fieldManager": {FieldManager}}, body)
	if err != nil {
		return model.Detail{}, err
	}
	if r.Status != http.StatusCreated && r.Status != http.StatusOK {
		return model.Detail{}, k.writeErr("create "+name, name, r)
	}
	var d Definition
	_ = json.Unmarshal(r.Body, &d)
	return BuiltDetail(d), nil
}

func (k *Kube) Update(ctx context.Context, name, rv string, spec model.Spec) (model.Detail, error) {
	cur, ke, err := k.def(ctx, name)
	if err != nil {
		return model.Detail{}, err
	}
	if ke != nil {
		if taken, err := k.takenByChart(ctx, name); err == nil && taken {
			return model.Detail{}, Errf(http.StatusForbidden, "read_only", "%s comes from chart values (agents[]). Change it in your values file and run install.sh.", name)
		}
		return model.Detail{}, ke
	}
	if rv == "" {
		return model.Detail{}, Errf(http.StatusBadRequest, "bad_request", "resourceVersion is required to update %s: send the one you read.", name)
	}
	if rv != cur.Meta.ResourceVersion {
		return model.Detail{}, Errf(http.StatusConflict, "conflict", "%s was changed by someone else (or by the controller) since you opened it. Reload it, then apply your changes again.", name)
	}
	// Read the whole object, replace only spec, and send it back with the resourceVersion the person read: a PUT built from
	// scratch would drop the finalizer and other metadata the controller keeps there.
	r, err := k.call(ctx, "read "+name, http.MethodGet, k.defsPath+"/"+url.PathEscape(name), nil, nil)
	if err != nil {
		return model.Detail{}, err
	}
	var full map[string]any
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &full) != nil {
		return model.Detail{}, k.fail("read "+name, r)
	}
	var specAny any
	b, _ := json.Marshal(spec)
	_ = json.Unmarshal(b, &specAny)
	full["spec"] = specAny
	delete(full, "status")
	if m, ok := full["metadata"].(map[string]any); ok {
		m["resourceVersion"] = rv
		delete(m, "managedFields")
	}
	body, _ := json.Marshal(full)
	r, err = k.call(ctx, "save "+name, http.MethodPut, k.defsPath+"/"+url.PathEscape(name), url.Values{"fieldManager": {FieldManager}}, body)
	if err != nil {
		return model.Detail{}, err
	}
	if r.Status != http.StatusOK {
		return model.Detail{}, k.writeErr("save "+name, name, r)
	}
	var d Definition
	_ = json.Unmarshal(r.Body, &d)
	return BuiltDetail(d), nil
}

func (k *Kube) Delete(ctx context.Context, name string) error {
	_, ke, err := k.def(ctx, name)
	if err != nil {
		return err
	}
	if ke != nil {
		if taken, err := k.takenByChart(ctx, name); err == nil && taken {
			return Errf(http.StatusForbidden, "read_only", "%s comes from chart values (agents[]). Remove it from your values file and run install.sh.", name)
		}
		return ke
	}
	r, err := k.call(ctx, "delete "+name, http.MethodDelete, k.defsPath+"/"+url.PathEscape(name), nil, nil)
	if err != nil {
		return err
	}
	if r.Status != http.StatusOK && r.Status != http.StatusAccepted && r.Status != http.StatusNoContent {
		return k.writeErr("delete "+name, name, r)
	}
	return nil
}

func (k *Kube) Schema(ctx context.Context, name string, spec model.Spec) (model.SchemaResult, error) {
	cur, ke, err := k.def(ctx, name)
	if err != nil {
		return model.SchemaResult{}, err
	}
	q := url.Values{"dryRun": {"All"}, "fieldManager": {FieldManager}}
	var r kube.Response
	if ke == nil {
		body, _ := k.object(name, spec, cur.Meta.ResourceVersion)
		r, err = k.call(ctx, "check "+name, http.MethodPut, k.defsPath+"/"+url.PathEscape(name), q, body)
	} else {
		body, _ := k.object(name, spec, "")
		r, err = k.call(ctx, "check "+name, http.MethodPost, k.defsPath, q, body)
	}
	if err != nil {
		return model.SchemaResult{}, err
	}
	switch r.Status {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
		return model.SchemaResult{OK: true, Errors: []string{}}, nil
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		e := k.writeErr("check "+name, name, r)
		var st statusObj
		_ = json.Unmarshal(r.Body, &st)
		msgs := []string{}
		for _, c := range st.Details.Causes {
			m := c.Message
			if c.Field != "" {
				m = c.Field + ": " + m
			}
			msgs = append(msgs, m)
		}
		if len(msgs) == 0 {
			msgs = []string{e.Message}
		}
		return model.SchemaResult{OK: false, Errors: msgs}, nil
	}
	return model.SchemaResult{}, k.writeErr("check "+name, name, r)
}
