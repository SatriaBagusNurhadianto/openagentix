// Package agentgateway is the ModelGateway adapter. It keeps one ConfigMap, llm-api-keys-control-plane, in the gateway
// namespace: one entry per gateway user, in the format install.sh writes into llm-api-keys. The ConfigMap is created by
// the chart (labelled agentgateway.dev/apikey=true, which the gateway policy selects on); the controller only patches
// single entries, so it never touches the installer's ConfigMap and the installer never touches this one.
package agentgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
)

// ConfigMapName is the controller's own key ConfigMap.
const ConfigMapName = "llm-api-keys-control-plane"

var cm = kube.Resource{Version: "v1", Plural: "configmaps", Kind: "ConfigMap", Namespaced: true}

var userRE = regexp.MustCompile(`^[a-z][-a-z0-9]{0,39}$`)

// API is the part of the REST client this adapter uses.
type API interface {
	Get(ctx context.Context, r kube.Resource, ns, name string) (kube.Object, error)
	PatchMerge(ctx context.Context, r kube.Resource, ns, name, sub string, patch kube.Object) error
}

type Adapter struct {
	api API
	ns  string
}

func New(api API, namespace string) *Adapter { return &Adapter{api: api, ns: namespace} }

func (a *Adapter) Name() string { return "agentgateway" }

// entry is the exact text install.sh writes: {"keyHash":"<hash>","metadata":{"user_id":"<user>"}}.
func entry(user, hash string) string {
	h, _ := json.Marshal(hash)
	u, _ := json.Marshal(user)
	return `{"keyHash":` + string(h) + `,"metadata":{"user_id":` + string(u) + `}}`
}

// Register sets the entry of user. A missing ConfigMap is an error: the chart creates it.
func (a *Adapter) Register(ctx context.Context, user, hash string) error {
	if !userRE.MatchString(user) {
		return fmt.Errorf("gateway user id %q is not a valid name", user)
	}
	err := a.api.PatchMerge(ctx, cm, a.ns, ConfigMapName, "", kube.Object{"data": kube.Object{user: entry(user, hash)}})
	if err != nil {
		return fmt.Errorf("write key entry for %s: %w", user, err)
	}
	return nil
}

// Unregister removes the entry of user; nothing to do when the ConfigMap or the entry is missing.
func (a *Adapter) Unregister(ctx context.Context, user string) error {
	err := a.api.PatchMerge(ctx, cm, a.ns, ConfigMapName, "", kube.Object{"data": kube.Object{user: nil}})
	if err != nil && !kube.IsNotFound(err) {
		return fmt.Errorf("remove key entry for %s: %w", user, err)
	}
	return nil
}

// Entries returns the hash of every user that has an entry ("" for an entry that is not in the installer's format).
func (a *Adapter) Entries(ctx context.Context) (map[string]string, error) {
	o, err := a.api.Get(ctx, cm, a.ns, ConfigMapName)
	if err != nil {
		return nil, fmt.Errorf("read key ConfigMap: %w", err)
	}
	data, _ := o["data"].(map[string]any)
	out := make(map[string]string, len(data))
	for user, v := range data {
		s, _ := v.(string)
		var e struct {
			KeyHash string `json:"keyHash"`
		}
		_ = json.Unmarshal([]byte(s), &e)
		out[user] = e.KeyHash
	}
	return out, nil
}
