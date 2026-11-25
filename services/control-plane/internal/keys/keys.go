// Package keys makes and registers the per-agent gateway virtual key. The key exists only in process memory and in the
// body of one create request; the controller never reads or lists Secrets, and no log line, status or error repeats it.
package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
)

// Outcome says what Ensure did.
type Outcome string

const (
	Created  Outcome = "created"  // Secret created (201), hash written
	Exists   Outcome = "exists"   // Secret exists and the gateway has a hash for the user: nothing to do
	Replaced Outcome = "replaced" // Secret existed without a hash: deleted, created again, hash written
)

// Mint returns "vk-" plus 16 random bytes in hex, like install.sh.
func Mint() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("random source failed")
	}
	return "vk-" + hex.EncodeToString(b), nil
}

// Hash is what the gateway stores: "sha256:" and the hex SHA-256 of the key.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Registry is the model gateway's key store.
type Registry interface {
	Register(ctx context.Context, user, hash string) error
	Registered(ctx context.Context, user string) (bool, error)
}

// Secrets is the write-only view of Secrets the controller has: create and delete, never get or list.
type Secrets interface {
	Create(ctx context.Context, ns string, obj kube.Object) error
	Delete(ctx context.Context, ns, name string) error
}

// KubeSecrets implements Secrets over the REST client.
type KubeSecrets struct{ C *kube.Client }

var secretRes = kube.Resource{Version: "v1", Plural: "secrets", Kind: "Secret", Namespaced: true}

func (k *KubeSecrets) Create(ctx context.Context, ns string, obj kube.Object) error {
	_, err := k.C.Create(ctx, secretRes, ns, obj)
	return err
}

func (k *KubeSecrets) Delete(ctx context.Context, ns, name string) error {
	return k.C.Delete(ctx, secretRes, ns, name)
}

type OwnerRef struct{ APIVersion, Kind, Name, UID string }

// Params describe the Secret and the gateway user.
type Params struct {
	Namespace, SecretName, DataKey, User string
	Labels                               map[string]string
	Owner                                OwnerRef
}

func (p Params) secret(key string) kube.Object {
	labels := map[string]any{}
	for k, v := range p.Labels {
		labels[k] = v
	}
	return kube.Object{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque", "immutable": true,
		"metadata": kube.Object{
			"name": p.SecretName, "namespace": p.Namespace, "labels": labels,
			"ownerReferences": []any{kube.Object{"apiVersion": p.Owner.APIVersion, "kind": p.Owner.Kind, "name": p.Owner.Name, "uid": p.Owner.UID, "controller": true}},
		},
		"data": kube.Object{p.DataKey: base64.StdEncoding.EncodeToString([]byte(key))},
	}
}

// Ensure makes sure the agent has a Secret and a registered hash, following ADR 0007 "Virtual keys".
func Ensure(ctx context.Context, p Params, sec Secrets, reg Registry, mint func() (string, error)) (Outcome, error) {
	create := func() (string, error) {
		key, err := mint()
		if err != nil {
			return "", err
		}
		return key, sec.Create(ctx, p.Namespace, p.secret(key))
	}
	key, err := create()
	switch {
	case err == nil:
		if err := reg.Register(ctx, p.User, Hash(key)); err != nil {
			return "", fmt.Errorf("register key hash for %s: %w", p.User, err)
		}
		return Created, nil
	case !kube.IsConflict(err):
		return "", fmt.Errorf("create key secret %s: %w", p.SecretName, err)
	}
	// 409: the Secret exists. Whether it is usable is known only from the gateway side: is there a hash for the user?
	ok, err := reg.Registered(ctx, p.User)
	if err != nil {
		return "", fmt.Errorf("check key registration for %s: %w", p.User, err)
	}
	if ok {
		return Exists, nil
	}
	if err := sec.Delete(ctx, p.Namespace, p.SecretName); err != nil && !kube.IsNotFound(err) {
		return "", fmt.Errorf("delete key secret %s: %w", p.SecretName, err)
	}
	if key, err = create(); err != nil {
		return "", fmt.Errorf("create key secret %s again: %w", p.SecretName, err)
	}
	if err := reg.Register(ctx, p.User, Hash(key)); err != nil {
		return "", fmt.Errorf("register key hash for %s: %w", p.User, err)
	}
	return Replaced, nil
}
