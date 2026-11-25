package agentgateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openagentix/openagentix/services/control-plane/internal/keys"
	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
	"github.com/openagentix/openagentix/services/control-plane/internal/kubetest"
)

func setup(t *testing.T) (*kubetest.Server, *Adapter) {
	s := kubetest.New()
	t.Cleanup(s.Close)
	s.Seed("configmaps", kubetest.Object{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": kubetest.Object{"name": ConfigMapName, "namespace": "agentgateway-system", "labels": kubetest.Object{"agentgateway.dev/apikey": "true"}}})
	c := kube.New(s.URL, s.Client(), func() (string, error) { return "t", nil })
	return s, New(c, "agentgateway-system")
}

var _ keys.Registry = (*Adapter)(nil)

func TestRegisterWritesTheInstallersEntryFormat(t *testing.T) {
	s, a := setup(t)
	if err := a.Register(context.Background(), "researcher", "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	cm, _ := s.Get("configmaps", "agentgateway-system", ConfigMapName)
	got := cm["data"].(map[string]any)["researcher"].(string)
	// install.sh: {"keyHash":"$h","metadata":{"user_id":"$agent"}}  (no spaces, this key order)
	want := `{"keyHash":"sha256:abc","metadata":{"user_id":"researcher"}}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if !json.Valid([]byte(got)) {
		t.Fatal("not JSON")
	}
	r := s.Matching("PATCH", "/configmaps/"+ConfigMapName)
	if len(r) != 1 || r[0].ContentType != "application/merge-patch+json" {
		t.Fatalf("%+v", s.Requests())
	}
	if cm["metadata"].(map[string]any)["labels"].(map[string]any)["agentgateway.dev/apikey"] != "true" {
		t.Fatal("label must survive")
	}
}

func TestEntriesAndUnregister(t *testing.T) {
	_, a := setup(t)
	ctx := context.Background()
	if m, err := a.Entries(ctx); err != nil || len(m) != 0 {
		t.Fatalf("%v %v", m, err)
	}
	_ = a.Register(ctx, "x", "sha256:1")
	_ = a.Register(ctx, "y", "sha256:2")
	m, err := a.Entries(ctx)
	if err != nil || m["x"] != "sha256:1" || m["y"] != "sha256:2" || len(m) != 2 {
		t.Fatalf("%v %v", m, err)
	}
	if err := a.Unregister(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	m, _ = a.Entries(ctx)
	if _, ok := m["x"]; ok {
		t.Fatal("x should be gone")
	}
	if m["y"] != "sha256:2" {
		t.Fatal("y must stay")
	}
}

func TestEntriesListsAnUnreadableEntryWithAnEmptyHash(t *testing.T) {
	s, a := setup(t)
	s.Modify("configmaps", "agentgateway-system", ConfigMapName, func(o kubetest.Object) {
		o["data"] = map[string]any{"junk": "not json", "empty": ""}
	})
	m, err := a.Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := m["junk"]; !ok || h != "" {
		t.Fatalf("an entry nobody can use must be listed (so it can be swept) with no hash: %v", m)
	}
}

func TestEntriesOfAMissingConfigMapIsAnError(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	a := New(kube.New(s.URL, s.Client(), func() (string, error) { return "t", nil }), "agentgateway-system")
	if _, err := a.Entries(context.Background()); err == nil {
		t.Fatal("want an error")
	}
}

func TestUnregisterOfMissingEntryIsFine(t *testing.T) {
	_, a := setup(t)
	if err := a.Unregister(context.Background(), "never"); err != nil {
		t.Fatal(err)
	}
}

func TestMissingConfigMapIsAnError(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	a := New(kube.New(s.URL, s.Client(), func() (string, error) { return "t", nil }), "agentgateway-system")
	if err := a.Register(context.Background(), "x", "sha256:1"); err == nil {
		t.Fatal("the chart creates the ConfigMap; a missing one is an error, not something to create")
	}
	if err := a.Unregister(context.Background(), "x"); err != nil {
		t.Fatalf("nothing to unregister when the map is gone: %v", err)
	}
}

func TestUserNamesThatCouldBreakTheEntryAreRefused(t *testing.T) {
	_, a := setup(t)
	if err := a.Register(context.Background(), `x","y`, "sha256:1"); err == nil {
		t.Fatal("want error")
	}
}
