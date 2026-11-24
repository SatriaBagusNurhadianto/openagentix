package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunServesAndStops(t *testing.T) {
	var gotUser string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-User-Id")
		io.WriteString(w, "{}")
	}))
	defer up.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "rules.json"), []byte(`{"callers":[{"name":"k","key":true,"agents":["kagent/assistant"],"userId":{"fixed":"kourier:k"}}]}`), 0o600)
	os.Mkdir(filepath.Join(dir, "keys"), 0o700)
	h := sha256.Sum256([]byte("the-key"))
	os.WriteFile(filepath.Join(dir, "keys", "k"), []byte(hex.EncodeToString(h[:])), 0o600)
	env := map[string]string{
		"A2A_GATE_UPSTREAM": up.URL, "A2A_GATE_RULES": filepath.Join(dir, "rules.json"), "A2A_GATE_KEYS_DIR": filepath.Join(dir, "keys"),
		"A2A_GATE_ADDR": "127.0.0.1:0",
	}
	var out, errb bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, func(k string) string { return env[k] }, deps{Stdout: &out, Log: slog.New(slog.NewJSONHandler(&errb, nil)), OnListen: func(a string) { addr <- a }, Reviewer: noReviewer{}})
	}()
	var a string
	select {
	case a = <-addr:
	case <-time.After(3 * time.Second):
		t.Fatal("did not start")
	}
	req, _ := http.NewRequest("POST", "http://"+a+"/agents/kagent/assistant", strings.NewReader(`{"params":{"message":{"contextId":"c1"}}}`))
	req.Header.Set("Authorization", "Bearer the-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", resp, err)
	}
	resp.Body.Close()
	if gotUser != "kourier:k" {
		t.Errorf("upstream user %q", gotUser)
	}
	resp, _ = http.Get("http://" + a + "/agents/kagent/assistant")
	if resp.StatusCode != 401 {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(out.String(), `"msg":"audit"`) || strings.Contains(out.String(), "the-key") {
		t.Errorf("audit output: %s", out.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}

type noReviewer struct{}

func (noReviewer) Review(context.Context, string, string) (string, error) {
	return "", errors.New("none")
}

func TestRunRefusesBadConfig(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" }, deps{Stdout: io.Discard, Log: slog.New(slog.DiscardHandler), Reviewer: noReviewer{}})
	if err == nil {
		t.Fatal("expected an error")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "rules.json"), []byte(`{"callers":[{"name":"a"}]}`), 0o600)
	env := map[string]string{"A2A_GATE_UPSTREAM": "http://x:1", "A2A_GATE_RULES": filepath.Join(dir, "rules.json")}
	err = run(context.Background(), func(k string) string { return env[k] }, deps{Stdout: io.Discard, Log: slog.New(slog.DiscardHandler), Reviewer: noReviewer{}})
	if err == nil || !strings.Contains(err.Error(), "rules") {
		t.Fatalf("invalid rules must stop the gate: %v", err)
	}
}
