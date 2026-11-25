package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func start(t *testing.T, env map[string]string) (addr string, errc chan error, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	errc = make(chan error, 1)
	go func() {
		errc <- run(ctx, func(k string) string { return env[k] }, deps{Stdout: io.Discard, Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), OnListen: func(a string) { ready <- a }})
	}()
	select {
	case addr = <-ready:
	case err := <-errc:
		cancel()
		t.Fatalf("run returned: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("not listening")
	}
	return addr, errc, func() {
		cancel()
		select {
		case <-errc:
		case <-time.After(5 * time.Second):
			t.Error("did not stop")
		}
	}
}

func TestDemoModeServesTheDemoData(t *testing.T) {
	addr, _, stop := start(t, map[string]string{"CONSOLE_DEV_FAKE": "1", "CONSOLE_ADDR": "127.0.0.1:0", "CONSOLE_DEV_FAKE_SETTLE": "0"})
	defer stop()
	get := func(path, tok string) (int, []byte) {
		req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if c, _ := get("/api/v1/agents", ""); c != 401 {
		t.Fatal("no token:", c)
	}
	c, b := get("/api/v1/session", "demo-token")
	var s struct{ Demo bool }
	_ = json.Unmarshal(b, &s)
	if c != 200 || !s.Demo {
		t.Fatal(c, string(b))
	}
	_, b = get("/api/v1/agents", "demo-token")
	var l struct {
		Items []struct{ Name, Source, State string }
	}
	_ = json.Unmarshal(b, &l)
	got := map[string]string{}
	for _, a := range l.Items {
		got[a.Name] = a.Source + "/" + a.State
	}
	want := map[string]string{"assistant": "chart/ready", "profile-demo": "chart/ready", "researcher": "built/ready", "cve-triage": "built/refused", "release-notes": "built/pending"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q want %q (all: %v)", k, got[k], v, got)
		}
	}
	if code, b := get("/", ""); code != 200 && code != 503 {
		t.Fatal(code, string(b))
	}
}

func TestDemoModeAndClusterSettingsDoNotMix(t *testing.T) {
	ctx := context.Background()
	err := run(ctx, func(k string) string {
		return map[string]string{"CONSOLE_DEV_FAKE": "1", "CONSOLE_KUBE_API": "https://kubernetes.default.svc"}[k]
	}, deps{Stdout: io.Discard, Log: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if err == nil || !strings.Contains(err.Error(), "refuses to start") {
		t.Fatal(err)
	}
}

func TestRealModeRefusesAnEmptyTokenFileAndMissingCatalog(t *testing.T) {
	dir := t.TempDir()
	w := func(name, content string) string {
		p := dir + "/" + name
		if err := writeFile(p, content); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := map[string]string{"CONSOLE_CATALOG_FILE": w("c.json", `{"namespaces":{"agents":"kagent"}}`), "CONSOLE_POLICY_FILE": w("p.json", `{}`),
		"CONSOLE_KUBE_API": "http://127.0.0.1:1", "CONSOLE_ADDR": "127.0.0.1:0"}
	d := deps{Stdout: &bytes.Buffer{}, Log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	m := map[string]string{"CONSOLE_TOKEN_FILE": w("t", "")}
	for k, v := range base {
		m[k] = v
	}
	if err := run(context.Background(), env(m), d); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatal("an empty token file must stop the start:", err)
	}
	m["CONSOLE_TOKEN_FILE"] = w("t2", "tok")
	m["CONSOLE_CATALOG_FILE"] = dir + "/missing.json"
	if err := run(context.Background(), env(m), d); err == nil || !strings.Contains(err.Error(), "platform files") {
		t.Fatal(err)
	}
}
