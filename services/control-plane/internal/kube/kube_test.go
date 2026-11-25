package kube_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
	"github.com/openagentix/openagentix/services/control-plane/internal/kubetest"
)

var defs = kube.Resource{Group: "openagentix.io", Version: "v1alpha1", Plural: "agentdefinitions", Namespaced: true}
var secrets = kube.Resource{Version: "v1", Plural: "secrets", Namespaced: true}

func newClient(t *testing.T, s *kubetest.Server) *kube.Client {
	t.Helper()
	c := kube.New(s.URL, s.Client(), func() (string, error) { return "tok-1", nil })
	return c
}

func def(name string) kubetest.Object {
	return kubetest.Object{"apiVersion": "openagentix.io/v1alpha1", "kind": "AgentDefinition",
		"metadata": kubetest.Object{"name": name, "namespace": "kagent"}, "spec": kubetest.Object{"model": "m"}}
}

func TestGetListSendBearerAndSelector(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	a := def("a")
	a["metadata"].(kubetest.Object)["labels"] = kubetest.Object{"k": "v"}
	s.Seed("agentdefinitions", a)
	s.Seed("agentdefinitions", def("b"))
	c := newClient(t, s)
	ctx := context.Background()
	got, err := c.Get(ctx, defs, "kagent", "a")
	if err != nil || got["metadata"].(map[string]any)["name"] != "a" {
		t.Fatalf("%v %v", got, err)
	}
	items, rv, err := c.List(ctx, defs, "kagent", "k=v")
	if err != nil || len(items) != 1 || rv == "" {
		t.Fatalf("%v %q %v", items, rv, err)
	}
	if _, err := c.Get(ctx, defs, "kagent", "none"); !kube.IsNotFound(err) {
		t.Fatalf("want NotFound, got %v", err)
	}
}

func TestTokenIsReadOnEveryRequest(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	n := 0
	seen := []string{}
	srv := s.Server
	c := kube.New(srv.URL, srv.Client(), func() (string, error) { n++; return "tok-" + string(rune('0'+n)), nil })
	s.Intercept = func(r kubetest.Request) (int, any, bool) {
		return 200, map[string]any{"items": []any{}, "metadata": map[string]any{}}, true
	}
	_ = seen
	for range 3 {
		if _, _, err := c.List(context.Background(), defs, "kagent", ""); err != nil {
			t.Fatal(err)
		}
	}
	if n != 3 {
		t.Fatalf("token read %d times, want once per request", n)
	}
}

func TestTokenFileReread(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(p, []byte("one\n"), 0o600)
	f := kube.FileToken(p)
	if v, _ := f(); v != "one" {
		t.Fatal(v)
	}
	_ = os.WriteFile(p, []byte("two\n"), 0o600)
	if v, _ := f(); v != "two" {
		t.Fatal(v)
	}
}

func TestApplySendsServerSideApplyBody(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	c := newClient(t, s)
	obj := kubetest.Object{"apiVersion": "api.kagent.dev/v1alpha3", "kind": "Harness", "metadata": kubetest.Object{"name": "x-harness", "namespace": "kagent"}, "spec": kubetest.Object{"a": "b"}}
	res := kube.Resource{Group: "api.kagent.dev", Version: "v1alpha3", Plural: "harnesses", Namespaced: true}
	if err := c.Apply(context.Background(), res, "kagent", "x-harness", obj); err != nil {
		t.Fatal(err)
	}
	reqs := s.Matching("PATCH", "/apis/api.kagent.dev/v1alpha3/namespaces/kagent/harnesses/x-harness")
	if len(reqs) != 1 {
		t.Fatalf("%v", s.Requests())
	}
	r := reqs[0]
	if r.ContentType != "application/apply-patch+yaml" || !strings.Contains(r.Query, "fieldManager=openagentix-control-plane") || !strings.Contains(r.Query, "force=true") {
		t.Fatalf("%+v", r)
	}
	var sent map[string]any
	if err := json.Unmarshal(r.Body, &sent); err != nil || sent["kind"] != "Harness" {
		t.Fatalf("body %s", r.Body)
	}
}

func TestPatchStatusIsMergePatchOnSubresource(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	s.Seed("agentdefinitions", def("a"))
	c := newClient(t, s)
	if err := c.PatchMerge(context.Background(), defs, "kagent", "a", "status", map[string]any{"status": map[string]any{"gatewayUserId": "a"}}); err != nil {
		t.Fatal(err)
	}
	r := s.Matching("PATCH", "/agentdefinitions/a/status")
	if len(r) != 1 || r[0].ContentType != "application/merge-patch+json" {
		t.Fatalf("%+v", s.Requests())
	}
	got, _ := s.Get("agentdefinitions", "kagent", "a")
	if got["status"].(map[string]any)["gatewayUserId"] != "a" {
		t.Fatalf("%v", got)
	}
}

func TestCreateConflictAndDelete(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	c := newClient(t, s)
	sec := kubetest.Object{"apiVersion": "v1", "kind": "Secret", "metadata": kubetest.Object{"name": "x"}}
	if _, err := c.Create(context.Background(), secrets, "kagent", sec); err != nil {
		t.Fatal(err)
	}
	_, err := c.Create(context.Background(), secrets, "kagent", sec)
	if !kube.IsConflict(err) {
		t.Fatalf("want conflict, got %v", err)
	}
	if err := c.Delete(context.Background(), secrets, "kagent", "x"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(context.Background(), secrets, "kagent", "x"); !kube.IsNotFound(err) {
		t.Fatalf("want NotFound, got %v", err)
	}
}

func TestErrorsCarryStatusAndReasonOnly(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	c := newClient(t, s)
	_, err := c.Get(context.Background(), defs, "kagent", "none")
	var e *kube.Error
	if !errors.As(err, &e) || e.Code != 404 || e.Reason != "NotFound" {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "fake message") {
		t.Fatalf("error text repeats the API's message: %q", err)
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	s.Seed("agentdefinitions", def("a"))
	c := newClient(t, s)
	o, _ := c.Get(context.Background(), defs, "kagent", "a")
	o["metadata"].(map[string]any)["finalizers"] = []any{"f"}
	out, err := c.Update(context.Background(), defs, "kagent", "a", o)
	if err != nil {
		t.Fatal(err)
	}
	if out["metadata"].(map[string]any)["finalizers"].([]any)[0] != "f" {
		t.Fatalf("%v", out)
	}
	// stale resourceVersion: conflict
	if _, err := c.Update(context.Background(), defs, "kagent", "a", o); !kube.IsConflict(err) {
		t.Fatalf("want conflict on a stale update, got %v", err)
	}
}

func collect(t *testing.T, st *kube.Stream, n int) []kube.Event {
	t.Helper()
	var out []kube.Event
	for len(out) < n {
		e, err := st.Next()
		if err != nil {
			t.Fatalf("after %d events: %v", len(out), err)
		}
		out = append(out, e)
	}
	return out
}

func TestWatchResumesByResourceVersion(t *testing.T) {
	s := kubetest.New()
	defer s.Close()
	s.Seed("agentdefinitions", def("a"))
	c := newClient(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, rv, _ := c.List(ctx, defs, "kagent", "")
	st, err := c.Watch(ctx, defs, "kagent", rv)
	if err != nil {
		t.Fatal(err)
	}
	s.Seed("agentdefinitions", def("b"))
	ev := collect(t, st, 1)
	if ev[0].Type != "ADDED" || ev[0].Object["metadata"].(map[string]any)["name"] != "b" {
		t.Fatalf("only events after the list's version arrive: %+v", ev)
	}
	if ev[0].ResourceVersion() == "" {
		t.Fatal("event has a resourceVersion")
	}
	if !strings.Contains(s.Matching("GET", "agentdefinitions")[len(s.Matching("GET", "agentdefinitions"))-1].Query, "resourceVersion="+rv) {
		t.Fatal("watch must send the resourceVersion")
	}
}

func TestWatchGoneBothWays(t *testing.T) {
	for _, mode := range []string{"http", "event"} {
		t.Run(mode, func(t *testing.T) {
			s := kubetest.New()
			defer s.Close()
			c := newClient(t, s)
			s.GoneNextWatch("agentdefinitions", "kagent", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st, err := c.Watch(ctx, defs, "kagent", "1")
			if err == nil {
				_, err = st.Next()
			}
			if !errors.Is(err, kube.ErrGone) {
				t.Fatalf("want ErrGone, got %v", err)
			}
		})
	}
}

func TestUnreachableServerErrorHasNoURLQuery(t *testing.T) {
	c := kube.New("http://127.0.0.1:1", http.DefaultClient, func() (string, error) { return "tok", nil })
	_, err := c.Get(context.Background(), defs, "kagent", "a")
	if err == nil || strings.Contains(err.Error(), "tok") {
		t.Fatalf("%v", err)
	}
}
