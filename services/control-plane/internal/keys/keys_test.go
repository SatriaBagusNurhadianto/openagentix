package keys

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
	"github.com/openagentix/openagentix/services/control-plane/internal/kubetest"
)

func TestMintShape(t *testing.T) {
	a, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Mint()
	if !regexp.MustCompile(`^vk-[0-9a-f]{32}$`).MatchString(a) || a == b {
		t.Fatalf("%q %q", a, b)
	}
}

func TestHashMatchesInstaller(t *testing.T) {
	// install.sh: printf %s key | shasum -a 256 | awk '{print "sha256:"$1}'
	sum := sha256.Sum256([]byte("vk-abc"))
	if got := Hash("vk-abc"); got != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal(got)
	}
}

type fakeRegistry struct {
	entries map[string]string
	err     error
}

func (f *fakeRegistry) Register(_ context.Context, user, hash string) error {
	if f.err != nil {
		return f.err
	}
	f.entries[user] = hash
	return nil
}

const secrets = "secrets"

func params() Params {
	return Params{Namespace: "kagent", SecretName: "r-virtual-key-0a1b2c3d", DataKey: "OPENAI_API_KEY", User: "r",
		Labels: map[string]string{"app.kubernetes.io/managed-by": "openagentix-control-plane", "openagentix.io/agent-definition": "r"},
		Owner:  OwnerRef{APIVersion: "openagentix.io/v1alpha1", Kind: "AgentDefinition", Name: "r", UID: "u-1"}}
}

func setup(t *testing.T) (*kubetest.Server, *KubeSecrets, *fakeRegistry) {
	s := kubetest.New()
	t.Cleanup(s.Close)
	c := kube.New(s.URL, s.Client(), func() (string, error) { return "t", nil })
	return s, &KubeSecrets{C: c}, &fakeRegistry{entries: map[string]string{}}
}

func fixed(key string) func() (string, error) { return func() (string, error) { return key, nil } }

func TestCreated201WritesHash(t *testing.T) {
	s, sec, reg := setup(t)
	res, err := Ensure(context.Background(), params(), sec, reg, fixed("vk-one"))
	if err != nil || res.Outcome != Created || res.Fingerprint != Fingerprint(Hash("vk-one")) {
		t.Fatalf("%+v %v", res, err)
	}
	if reg.entries["r"] != Hash("vk-one") {
		t.Fatalf("%v", reg.entries)
	}
	o, ok := s.Get(secrets, "kagent", "r-virtual-key-0a1b2c3d")
	if !ok {
		t.Fatal("secret not created")
	}
	if o["immutable"] != true || o["type"] != "Opaque" {
		t.Fatalf("%v", o)
	}
	d := o["data"].(map[string]any)["OPENAI_API_KEY"].(string)
	if raw, _ := base64.StdEncoding.DecodeString(d); string(raw) != "vk-one" {
		t.Fatalf("data %q", d)
	}
	m := o["metadata"].(map[string]any)
	if m["labels"].(map[string]any)["openagentix.io/agent-definition"] != "r" {
		t.Fatalf("labels %v", m["labels"])
	}
	or := m["ownerReferences"].([]any)[0].(map[string]any)
	if or["kind"] != "AgentDefinition" || or["uid"] != "u-1" || or["controller"] != true {
		t.Fatalf("owner %v", or)
	}
}

func TestConflictWithHashPresentAndNothingKnownAdoptsIt(t *testing.T) {
	s, sec, reg := setup(t)
	s.Seed(secrets, kubetest.Object{"metadata": kubetest.Object{"name": "r-virtual-key-0a1b2c3d", "namespace": "kagent"}})
	reg.entries["r"] = "sha256:0123456789abcdef0123456789abcdef"
	p := params()
	p.Registered = "sha256:0123456789abcdef0123456789abcdef"
	res, err := Ensure(context.Background(), p, sec, reg, fixed("vk-two"))
	if err != nil || res.Outcome != Exists || res.Fingerprint != "0123456789abcdef" {
		t.Fatalf("%+v %v", res, err)
	}
	if reg.entries["r"] != "sha256:0123456789abcdef0123456789abcdef" {
		t.Fatal("hash must be left alone")
	}
	if len(s.Matching("DELETE", "secrets")) != 0 {
		t.Fatal("must not delete")
	}
}

func TestKnownFingerprintMatchingTheRegisteredHashSendsNothing(t *testing.T) {
	s, sec, reg := setup(t)
	p := params()
	p.Registered = "sha256:0123456789abcdef0123456789abcdef"
	p.Known = "0123456789abcdef"
	minted := false
	res, err := Ensure(context.Background(), p, sec, reg, func() (string, error) { minted = true; return "vk-x", nil })
	if err != nil || res.Outcome != Unchanged || res.Fingerprint != "0123456789abcdef" {
		t.Fatalf("%+v %v", res, err)
	}
	if minted || len(s.Requests()) != 0 {
		t.Fatalf("a key that is in order costs no mint and no request: minted=%v, requests=%v", minted, s.Requests())
	}
}

func TestRegisteredHashThatIsNotTheKnownOneReplacesTheKey(t *testing.T) {
	s, sec, reg := setup(t)
	s.Seed(secrets, kubetest.Object{"metadata": kubetest.Object{"name": "r-virtual-key-0a1b2c3d", "namespace": "kagent"}, "data": kubetest.Object{"OPENAI_API_KEY": "b2xk"}})
	p := params()
	p.Registered = "sha256:ffffffffffffffff0000000000000000" // an older hash was restored into the gateway's map
	p.Known = "0123456789abcdef"
	res, err := Ensure(context.Background(), p, sec, reg, fixed("vk-new"))
	if err != nil || res.Outcome != Replaced || res.Fingerprint != Fingerprint(Hash("vk-new")) {
		t.Fatalf("%+v %v", res, err)
	}
	if reg.entries["r"] != Hash("vk-new") {
		t.Fatalf("%v", reg.entries)
	}
	o, _ := s.Get(secrets, "kagent", "r-virtual-key-0a1b2c3d")
	if raw, _ := base64.StdEncoding.DecodeString(o["data"].(map[string]any)["OPENAI_API_KEY"].(string)); string(raw) != "vk-new" {
		t.Fatalf("secret was not replaced: %q", raw)
	}
}

func TestConflictWithoutHashDeletesOwnSecretAndRecreates(t *testing.T) {
	s, sec, reg := setup(t)
	s.Seed(secrets, kubetest.Object{"metadata": kubetest.Object{"name": "r-virtual-key-0a1b2c3d", "namespace": "kagent"}, "data": kubetest.Object{"OPENAI_API_KEY": "b2xk"}})
	res, err := Ensure(context.Background(), params(), sec, reg, fixed("vk-three"))
	if err != nil || res.Outcome != Replaced {
		t.Fatalf("%+v %v", res, err)
	}
	if reg.entries["r"] != Hash("vk-three") {
		t.Fatalf("%v", reg.entries)
	}
	o, _ := s.Get(secrets, "kagent", "r-virtual-key-0a1b2c3d")
	if raw, _ := base64.StdEncoding.DecodeString(o["data"].(map[string]any)["OPENAI_API_KEY"].(string)); string(raw) != "vk-three" {
		t.Fatalf("secret was not replaced: %q", raw)
	}
	var seq []string
	for _, r := range s.Requests() {
		seq = append(seq, r.Method)
	}
	if strings.Join(seq, ",") != "POST,DELETE,POST" {
		t.Fatalf("want create, delete, create; got %v", seq)
	}
}

func TestSecretCarriesTheFingerprintAsAnAnnotation(t *testing.T) {
	s, sec, reg := setup(t)
	if _, err := Ensure(context.Background(), params(), sec, reg, fixed("vk-one")); err != nil {
		t.Fatal(err)
	}
	o, _ := s.Get(secrets, "kagent", "r-virtual-key-0a1b2c3d")
	a, _ := o["metadata"].(map[string]any)["annotations"].(map[string]any)
	if a["openagentix.io/key-fingerprint"] != Fingerprint(Hash("vk-one")) {
		t.Fatalf("annotations %v", a)
	}
}

func TestNeverReadsSecrets(t *testing.T) {
	s, sec, reg := setup(t)
	s.Seed(secrets, kubetest.Object{"metadata": kubetest.Object{"name": "r-virtual-key-0a1b2c3d", "namespace": "kagent"}})
	_, _ = Ensure(context.Background(), params(), sec, reg, fixed("vk-x"))
	for _, r := range s.Requests() {
		if r.Method == "GET" {
			t.Fatalf("GET on secrets: %+v", r)
		}
	}
}

func TestKeyNeverInErrors(t *testing.T) {
	_, sec, reg := setup(t)
	reg.err = errors.New("registry down")
	const canary = "vk-CANARYCANARYCANARYCANARY00000000"
	_, err := Ensure(context.Background(), params(), sec, reg, fixed(canary))
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte(canary))) {
		t.Fatalf("error leaks the key: %v", err)
	}
}

func TestSecondCreateConflictIsAnError(t *testing.T) {
	s, sec, reg := setup(t)
	s.Seed(secrets, kubetest.Object{"metadata": kubetest.Object{"name": "r-virtual-key-0a1b2c3d", "namespace": "kagent"}})
	s.Intercept = func(r kubetestRequest) (int, any, bool) {
		if r.Method == "DELETE" {
			return 200, map[string]any{}, true // delete "succeeds" but nothing is removed
		}
		return 0, nil, false
	}
	if _, err := Ensure(context.Background(), params(), sec, reg, fixed("vk-y")); err == nil {
		t.Fatal("a second conflict must fail, not loop")
	}
}

type kubetestRequest = kubetest.Request
