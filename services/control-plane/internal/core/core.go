// Package core is the level-triggered reconcile loop. It validates a definition against the catalog and the policy, mints
// and registers the agent's gateway key, asks the Runtime adapter for the objects, writes them, prunes what is no longer
// wanted and reports conditions. It knows the adapters only through the small interfaces below and never imports an
// adapter package (a test enforces it).
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openagentix/openagentix/services/control-plane/internal/api"
	"github.com/openagentix/openagentix/services/control-plane/internal/catalog"
	"github.com/openagentix/openagentix/services/control-plane/internal/keys"
	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
	"github.com/openagentix/openagentix/services/control-plane/internal/plan"
	"github.com/openagentix/openagentix/services/control-plane/internal/policy"
)

// Runtime turns a plan into the objects of an agent runtime and reports on them.
type Runtime interface {
	Name() string
	// OwnedKinds lists the endpoints the core lists by label to prune what is no longer wanted.
	OwnedKinds() []kube.Resource
	Desired(p plan.Plan) ([]kube.Manifest, error)
	Ref(p plan.Plan) api.ObjectRef
	CallEndpoint(p plan.Plan) string
	Readiness(ctx context.Context, p plan.Plan) (plan.Readiness, error)
}

// ModelGateway registers the hash of an agent's key under a gateway user.
type ModelGateway interface {
	Name() string
	keys.Registry
	Unregister(ctx context.Context, user string) error
}

// Kube is the part of the REST client the core uses.
type Kube interface {
	Get(ctx context.Context, r kube.Resource, ns, name string) (kube.Object, error)
	List(ctx context.Context, r kube.Resource, ns, selector string) ([]kube.Object, string, error)
	Watch(ctx context.Context, r kube.Resource, ns, rv string) (*kube.Stream, error)
	Apply(ctx context.Context, r kube.Resource, ns, name string, obj kube.Object) error
	PatchMerge(ctx context.Context, r kube.Resource, ns, name, sub string, patch kube.Object) error
	Update(ctx context.Context, r kube.Resource, ns, name string, obj kube.Object) (kube.Object, error)
	Delete(ctx context.Context, r kube.Resource, ns, name string) error
}

var definitions = kube.Resource{Group: api.Group, Version: api.Version, Plural: api.Plural, Kind: api.Kind, Namespaced: true}

// Definitions is the endpoint of AgentDefinition, for callers that build a client.
func Definitions() kube.Resource { return definitions }

type Config struct {
	Namespace string // the only namespace reconciled
	Catalog   *catalog.Catalog
	Policy    *catalog.Policy
	Kube      Kube
	Secrets   keys.Secrets
	Runtime   Runtime
	Gateway   ModelGateway
	Tools     catalog.ToolResolver
	Log       *slog.Logger
	Now       func() time.Time
	Mint      func() (string, error)
	Resync    time.Duration // full pass at least this often (60 s)
	Backoff   time.Duration // retry interval while something is unfinished or failed
}

type Controller struct {
	cfg   Config
	mu    sync.Mutex // one reconcile at a time
	seen  map[string]int64
	last  map[string]string
	smu   sync.Mutex
	ready atomic.Bool
	retry atomic.Bool
}

// New builds a controller; zero Config fields get defaults.
func New(cfg Config) *Controller {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Mint == nil {
		cfg.Mint = keys.Mint
	}
	if cfg.Resync <= 0 {
		cfg.Resync = 60 * time.Second
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = 10 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewJSONHandler(discard{}, nil))
	}
	return &Controller{cfg: cfg, seen: map[string]int64{}, last: map[string]string{}}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Ready reports whether the first full pass succeeded.
func (c *Controller) Ready() bool { return c.ready.Load() }

// ---- loop ----

// Run lists, reconciles, then watches; any change to a definition's spec, a new or a deleted definition, a 410 Gone, an
// error or the resync timer starts the next pass. It returns when ctx is done.
func (c *Controller) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		rv, err := c.reconcile(ctx)
		if rv == "" {
			c.cfg.Log.Error("list failed", "outcome", "list_failed", "err", err)
			sleep(ctx, c.cfg.Backoff)
			continue
		}
		if err != nil {
			c.cfg.Log.Error("pass finished with errors", "outcome", "retry", "err", err)
		}
		c.ready.Store(true)
		wait := c.cfg.Resync
		if c.retry.Load() {
			wait = c.cfg.Backoff
		}
		c.watch(ctx, rv, wait)
	}
	return ctx.Err()
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (c *Controller) watch(ctx context.Context, rv string, wait time.Duration) {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	started := time.Now()
	st, err := c.cfg.Kube.Watch(wctx, definitions, c.cfg.Namespace, rv)
	if err != nil {
		if !errors.Is(err, kube.ErrGone) {
			c.cfg.Log.Warn("watch failed", "outcome", "watch_failed", "err", err)
			sleep(ctx, c.cfg.Backoff)
		}
		return
	}
	defer st.Close()
	evs := make(chan kube.Event)
	errc := make(chan error, 1)
	go func() {
		for {
			e, err := st.Next()
			if err != nil {
				errc <- err
				return
			}
			select {
			case evs <- e:
			case <-wctx.Done():
				return
			}
		}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case e := <-evs:
			if c.relevant(e) {
				return
			}
		case err := <-errc:
			if ctx.Err() == nil && !errors.Is(err, kube.ErrGone) {
				c.cfg.Log.Debug("watch ended", "err", err)
			}
			if time.Since(started) < c.cfg.Backoff {
				sleep(ctx, c.cfg.Backoff)
			}
			return
		}
	}
}

// relevant filters out the events of the controller's own writes (status, finalizer): only a new definition, a changed
// spec (generation) or a requested deletion starts a pass.
func (c *Controller) relevant(e kube.Event) bool {
	if e.Type != "ADDED" && e.Type != "MODIFIED" {
		return false
	}
	m := kube.Meta(e.Object)
	if m.String("deletionTimestamp") != "" {
		return true
	}
	g, _ := m["generation"].(float64)
	c.smu.Lock()
	defer c.smu.Unlock()
	old, ok := c.seen[m.String("name")]
	return !ok || old != int64(g)
}

// ---- one pass ----

// ReconcileAll lists the definitions and reconciles each, one at a time. It returns an error only for failures that are
// not already reported in a definition's status (for example a finalizer that could not be removed).
func (c *Controller) ReconcileAll(ctx context.Context) error {
	_, err := c.reconcile(ctx)
	return err
}

func (c *Controller) reconcile(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.retry.Store(false)
	items, rv, err := c.cfg.Kube.List(ctx, definitions, c.cfg.Namespace, "")
	if err != nil {
		return "", err
	}
	slices.SortStableFunc(items, func(a, b kube.Object) int {
		ma, mb := kube.Meta(a), kube.Meta(b)
		if x := strings.Compare(ma.String("creationTimestamp"), mb.String("creationTimestamp")); x != 0 {
			return x
		}
		return strings.Compare(ma.String("name"), mb.String("name"))
	})
	c.smu.Lock()
	c.seen = map[string]int64{}
	for _, it := range items {
		g, _ := kube.Meta(it)["generation"].(float64)
		c.seen[kube.Meta(it).String("name")] = int64(g)
	}
	c.smu.Unlock()
	var errs []error
	var earlier []string
	for _, it := range items {
		d, err := api.FromObject(it)
		if err != nil {
			c.cfg.Log.Warn("skipping an object that is not a valid AgentDefinition", "outcome", "undecodable", "err", err)
			continue
		}
		if err := c.one(ctx, it, d, earlier); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.Metadata.Name, err))
			c.retry.Store(true)
		}
		if !d.Deleting() {
			earlier = append(earlier, d.Metadata.Name)
		}
	}
	return rv, errors.Join(errs...)
}

func selector(name string) string {
	return api.LabelDefinition + "=" + name + "," + api.LabelManagedBy + "=" + api.ManagedByValue
}

func (c *Controller) logOutcome(name, outcome string, kv ...any) {
	c.smu.Lock()
	changed := c.last[name] != outcome
	c.last[name] = outcome
	c.smu.Unlock()
	lvl := slog.LevelDebug
	if changed {
		lvl = slog.LevelInfo
	}
	c.cfg.Log.Log(context.Background(), lvl, "reconciled", append([]any{"agent", name, "outcome", outcome}, kv...)...)
}

func (c *Controller) one(ctx context.Context, raw kube.Object, d *api.AgentDefinition, earlier []string) error {
	name := d.Metadata.Name
	if d.Deleting() {
		return c.finalize(ctx, d)
	}
	if !d.HasFinalizer(api.FinalizerGatewayKey) {
		updated, err := c.addFinalizer(ctx, raw, name)
		if err != nil {
			return fmt.Errorf("add finalizer: %w", err)
		}
		if nd, err := api.FromObject(updated); err == nil {
			d = nd
		}
	}
	now := c.cfg.Now().UTC().Format(time.RFC3339)
	gen := d.Metadata.Generation
	st := cloneStatus(d.Status)
	st.ObservedGeneration = gen
	set := func(t, status, reason, msg string) {
		st.SetCondition(api.Condition{Type: t, Status: status, Reason: reason, Message: msg}, now, gen)
	}
	unknownIfAbsent := func(t, reason string) {
		if st.Condition(t).Type == "" {
			set(t, "Unknown", reason, "")
		}
	}
	finish := func(outcome string, kv ...any) error {
		c.logOutcome(name, outcome, kv...)
		if outcome != "ready" && outcome != "refused" {
			c.retry.Store(true)
		}
		return c.patchStatus(ctx, d, st)
	}

	// 1. accept or refuse
	violations := policy.Validate(policy.Input{Def: d, Catalog: c.cfg.Catalog, Policy: c.cfg.Policy, Tools: c.cfg.Tools, Earlier: earlier})
	if len(violations) > 0 {
		st.Violations = violations
		set(api.CondAccepted, "False", "PolicyViolation", fmt.Sprintf("%d rule(s) refused; see status.violations", len(violations)))
		unknownIfAbsent(api.CondKeyReady, "NotAccepted")
		set(api.CondApplied, "False", "NotAccepted", "the definition is refused; objects of an earlier version, if any, are left as they were")
		set(api.CondReady, "False", "NotAccepted", "")
		return finish("refused", "violations", len(violations))
	}
	st.Violations = nil
	set(api.CondAccepted, "True", "Accepted", "")

	p := plan.Build(d, c.cfg.Catalog, c.cfg.Tools)
	manifests, err := c.cfg.Runtime.Desired(p)
	if err != nil {
		set(api.CondApplied, "False", "RenderFailed", err.Error())
		set(api.CondReady, "False", "NotApplied", "")
		return finish("render_failed", "err", err)
	}
	owner := keys.OwnerRef{APIVersion: api.APIVersion, Kind: api.Kind, Name: name, UID: d.Metadata.UID}
	labels := map[string]string{api.LabelManagedBy: api.ManagedByValue, api.LabelDefinition: name}
	for _, m := range manifests {
		stamp(m.Object, labels, owner)
	}

	// 2. name conflicts: an object with a target name that we do not own stops everything, before any write
	if conflict, err := c.conflicts(ctx, name, manifests); err != nil {
		set(api.CondApplied, "False", "CheckFailed", err.Error())
		set(api.CondReady, "False", "NotApplied", "")
		return finish("check_failed", "err", err)
	} else if conflict != "" {
		unknownIfAbsent(api.CondKeyReady, "NotApplied")
		set(api.CondApplied, "False", "NameConflict", conflict+" already exists and is not owned by this definition; nothing was written")
		set(api.CondReady, "False", "NotApplied", "")
		return finish("name_conflict", "object", conflict)
	}

	// 3. the key
	out, err := keys.Ensure(ctx, keys.Params{
		Namespace: p.Namespace, SecretName: p.KeySecret, DataKey: p.KeySecretKey, User: p.GatewayUserID,
		Labels: labels, Owner: owner,
	}, c.cfg.Secrets, c.cfg.Gateway, c.cfg.Mint)
	if err != nil {
		set(api.CondKeyReady, "False", "KeyFailed", err.Error())
		set(api.CondApplied, "False", "NotApplied", "waiting for the gateway key")
		set(api.CondReady, "False", "NotApplied", "")
		return finish("key_failed", "secret", p.KeySecret, "err", err)
	}
	set(api.CondKeyReady, "True", "KeyReady", "Secret "+p.KeySecret)

	// 4. apply, then prune
	keep := map[string]bool{}
	var names []string
	for _, m := range manifests {
		n := kube.Meta(m.Object).String("name")
		if err := c.cfg.Kube.Apply(ctx, m.Resource, p.Namespace, n, m.Object); err != nil {
			set(api.CondApplied, "False", "ApplyFailed", fmt.Sprintf("%s %s: %v", m.Resource.Kind, n, err))
			set(api.CondReady, "False", "NotApplied", "")
			return finish("apply_failed", "kind", m.Resource.Kind, "object", n, "err", err)
		}
		keep[m.Resource.Plural+"/"+n] = true
		names = append(names, m.Resource.Kind+"/"+n)
	}
	if _, err := c.prune(ctx, name, keep); err != nil {
		set(api.CondApplied, "False", "PruneFailed", err.Error())
		set(api.CondReady, "False", "NotApplied", "")
		return finish("prune_failed", "err", err)
	}
	set(api.CondApplied, "True", "Applied", "")
	ref := c.cfg.Runtime.Ref(p)
	st.AgentRef = &ref
	st.GatewayUserID = p.GatewayUserID
	st.ToolServer = p.ToolServer
	st.EffectiveTools = slices.Clone(p.Tools)
	st.EffectiveEgress = slices.Clone(p.Egress)
	st.A2AURL = c.cfg.Runtime.CallEndpoint(p)

	// 5. readiness
	rd, err := c.cfg.Runtime.Readiness(ctx, p)
	switch {
	case err != nil:
		set(api.CondReady, "False", "ReadinessUnknown", err.Error())
		return finish("readiness_unknown", "key", string(out), "objects", names, "err", err)
	case rd.Ready:
		set(api.CondReady, "True", "Ready", "")
		return finish("ready", "key", string(out), "objects", names)
	default:
		set(api.CondReady, "False", rd.Reason, rd.Message)
		return finish("not_ready", "reason", rd.Reason, "objects", names)
	}
}

func cloneStatus(s api.Status) api.Status {
	s.Conditions = slices.Clone(s.Conditions)
	s.EffectiveTools = slices.Clone(s.EffectiveTools)
	s.EffectiveEgress = slices.Clone(s.EffectiveEgress)
	s.Violations = slices.Clone(s.Violations)
	if s.AgentRef != nil {
		r := *s.AgentRef
		s.AgentRef = &r
	}
	return s
}

// patchStatus writes the status when it differs from what is stored.
func (c *Controller) patchStatus(ctx context.Context, d *api.AgentDefinition, st api.Status) error {
	if reflect.DeepEqual(jsonMap(d.Status.Patch()), jsonMap(st.Patch())) {
		return nil
	}
	if err := c.cfg.Kube.PatchMerge(ctx, definitions, d.Metadata.Namespace, d.Metadata.Name, "status", st.Patch()); err != nil {
		return fmt.Errorf("patch status: %w", err)
	}
	return nil
}

func jsonMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func stamp(o kube.Object, labels map[string]string, owner keys.OwnerRef) {
	m := kube.Meta(o)
	l, _ := m["labels"].(map[string]any)
	if l == nil {
		l = map[string]any{}
	}
	for k, v := range labels {
		l[k] = v
	}
	m["labels"] = l
	m["ownerReferences"] = []any{map[string]any{"apiVersion": owner.APIVersion, "kind": owner.Kind, "name": owner.Name, "uid": owner.UID, "controller": true}}
}

func (c *Controller) conflicts(ctx context.Context, name string, ms []kube.Manifest) (string, error) {
	for _, m := range ms {
		n := kube.Meta(m.Object).String("name")
		cur, err := c.cfg.Kube.Get(ctx, m.Resource, c.cfg.Catalog.Namespaces.Agents, n)
		if kube.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("check %s %s: %w", m.Resource.Kind, n, err)
		}
		l, _ := kube.Meta(cur)["labels"].(map[string]any)
		if l[api.LabelManagedBy] != api.ManagedByValue || l[api.LabelDefinition] != name {
			return m.Resource.Kind + " " + n, nil
		}
	}
	return "", nil
}

// prune deletes the objects that carry this definition's labels and are not in keep ("plural/name").
func (c *Controller) prune(ctx context.Context, name string, keep map[string]bool) ([]string, error) {
	var deleted []string
	for _, kind := range c.cfg.Runtime.OwnedKinds() {
		items, _, err := c.cfg.Kube.List(ctx, kind, c.cfg.Catalog.Namespaces.Agents, selector(name))
		if err != nil {
			return deleted, fmt.Errorf("list %s: %w", kind.Plural, err)
		}
		for _, it := range items {
			n := kube.Meta(it).String("name")
			if keep[kind.Plural+"/"+n] {
				continue
			}
			if err := c.cfg.Kube.Delete(ctx, kind, c.cfg.Catalog.Namespaces.Agents, n); err != nil && !kube.IsNotFound(err) {
				return deleted, fmt.Errorf("delete %s %s: %w", kind.Kind, n, err)
			}
			deleted = append(deleted, kind.Kind+"/"+n)
			c.cfg.Log.Info("pruned", "agent", name, "kind", kind.Kind, "object", n, "outcome", "pruned")
		}
	}
	return deleted, nil
}

func (c *Controller) addFinalizer(ctx context.Context, raw kube.Object, name string) (kube.Object, error) {
	m := kube.Meta(raw)
	fin, _ := m["finalizers"].([]any)
	m["finalizers"] = append(fin, api.FinalizerGatewayKey)
	return c.cfg.Kube.Update(ctx, definitions, c.cfg.Namespace, name, raw)
}

// finalize runs when deletion was requested: owned objects and the key hash go, then the finalizer. The key Secret is
// removed by the owner reference.
func (c *Controller) finalize(ctx context.Context, d *api.AgentDefinition) error {
	name := d.Metadata.Name
	if !d.HasFinalizer(api.FinalizerGatewayKey) {
		return nil
	}
	if _, err := c.prune(ctx, name, nil); err != nil {
		return fmt.Errorf("remove objects: %w", err)
	}
	if err := c.cfg.Gateway.Unregister(ctx, name); err != nil {
		return fmt.Errorf("unregister key: %w", err)
	}
	cur, err := c.cfg.Kube.Get(ctx, definitions, c.cfg.Namespace, name)
	if kube.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read before removing finalizer: %w", err)
	}
	m := kube.Meta(cur)
	var rest []any
	for _, f := range m["finalizers"].([]any) {
		if f != api.FinalizerGatewayKey {
			rest = append(rest, f)
		}
	}
	m["finalizers"] = rest
	if rest == nil {
		delete(m, "finalizers")
	}
	if _, err := c.cfg.Kube.Update(ctx, definitions, c.cfg.Namespace, name, cur); err != nil && !kube.IsNotFound(err) {
		return fmt.Errorf("remove finalizer: %w", err)
	}
	c.logOutcome(name, "deleted")
	return nil
}
