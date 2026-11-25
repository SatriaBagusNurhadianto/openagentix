package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/control-plane/internal/kube"
	"github.com/openagentix/openagentix/services/control-plane/internal/kubetest"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func getenv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func baseEnv() map[string]string {
	return map[string]string{
		"CONTROL_PLANE_CATALOG_FILE": "../../testdata/catalog.json",
		"CONTROL_PLANE_POLICY_FILE":  "../../testdata/policy.json",
		"CONTROL_PLANE_HEALTH_ADDR":  "127.0.0.1:0",
		"CONTROL_PLANE_RESYNC":       "1s",
	}
}

func TestRunServesHealthReconcilesAndStopsOnCancel(t *testing.T) {
	srv := kubetest.New()
	defer srv.Close()
	srv.Seed("configmaps", kubetest.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": kubetest.Object{"name": "llm-api-keys-control-plane", "namespace": "agentgateway-system"}})
	srv.Seed("agentdefinitions", kubetest.Object{"apiVersion": "openagentix.io/v1alpha1", "kind": "AgentDefinition",
		"metadata": kubetest.Object{"name": "researcher", "namespace": "kagent", "creationTimestamp": "2026-11-25T10:00:00Z"},
		"spec":     kubetest.Object{"model": "claude-haiku", "instructions": "Be brief.", "toolProfile": "lookup", "tools": []any{"osv_query_vulnerability"}}})
	logs := &lockedBuf{}
	addr := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, getenv(baseEnv()), deps{
			Log:      slog.New(slog.NewJSONHandler(logs, nil)),
			Kube:     kube.New(srv.URL, srv.Client(), func() (string, error) { return "t", nil }),
			OnListen: func(a string) { addr <- a },
		})
	}()
	base := "http://" + <-addr
	if code := get(t, base+"/healthz"); code != 200 {
		t.Fatalf("healthz %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	for get(t, base+"/readyz") != 200 {
		if time.Now().After(deadline) {
			t.Fatal("never ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// the definition was reconciled with the real adapters: the five kagent objects and the gateway entry
	for _, n := range [][2]string{{"modelconfigs", "gateway-researcher"}, {"harnesses", "researcher-harness"}, {"agenttemplates", "researcher-template"}, {"agents", "researcher"}} {
		if _, ok := srv.Get(n[0], "kagent", n[1]); !ok {
			t.Fatalf("%s/%s missing", n[0], n[1])
		}
	}
	cm, _ := srv.Get("configmaps", "agentgateway-system", "llm-api-keys-control-plane")
	if e, _ := cm["data"].(map[string]any)["researcher"].(string); !strings.HasPrefix(e, `{"keyHash":"sha256:`) {
		t.Fatalf("gateway entry %v", cm["data"])
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not shut down")
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			t.Fatalf("log line not JSON: %q", line)
		}
	}
	if strings.Contains(logs.String(), "vk-") {
		t.Fatal("a key-shaped value is in the log")
	}
}

func get(t *testing.T, url string) int {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		return 0
	}
	defer r.Body.Close()
	_, _ = io.Copy(io.Discard, r.Body)
	return r.StatusCode
}

func TestRunRefusesBadSetup(t *testing.T) {
	dir := t.TempDir()
	badCat := filepath.Join(dir, "cat.json")
	b, _ := os.ReadFile("../../testdata/catalog.json")
	_ = os.WriteFile(badCat, []byte(strings.Replace(string(b), `"runtime": "kagent"`, `"runtime": "ax"`, 1)), 0o600)
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"no files":            {map[string]string{}, "CONTROL_PLANE_CATALOG_FILE"},
		"missing catalog":     {map[string]string{"CONTROL_PLANE_CATALOG_FILE": filepath.Join(dir, "none.json"), "CONTROL_PLANE_POLICY_FILE": "../../testdata/policy.json"}, "catalog"},
		"unsupported runtime": {map[string]string{"CONTROL_PLANE_CATALOG_FILE": badCat, "CONTROL_PLANE_POLICY_FILE": "../../testdata/policy.json"}, `adapter "ax"`},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), getenv(tc.env), deps{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), Kube: kube.New("http://127.0.0.1:1", http.DefaultClient, func() (string, error) { return "", nil })})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
