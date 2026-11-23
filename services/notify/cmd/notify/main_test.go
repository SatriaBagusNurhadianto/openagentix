package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type running struct {
	addr   string
	cancel context.CancelFunc
	done   chan error
	logs   *bytes.Buffer
}

func start(t *testing.T, env map[string]string) *running {
	t.Helper()
	env["NOTIFY_ADDR"] = "127.0.0.1:0"
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }, log, func(a string) { ready <- a }) }()
	select {
	case addr := <-ready:
		r := &running{addr: addr, cancel: cancel, done: done, logs: logs}
		t.Cleanup(func() { cancel(); <-done })
		return r
	case err := <-done:
		cancel()
		t.Fatalf("run returned early: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("not listening")
	}
	return nil
}

func status(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "destinations.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const cfgYAML = `
destinations:
  - name: ops
    description: On-call chat
    platform: telegram
    chatId: "-1002223334444"
    tokenEnv: TG_TOKEN
`

func TestNoConfigFile_StartsDegradedAndRefusesSends(t *testing.T) {
	r := start(t, map[string]string{})
	if code, _ := status(t, r.addr, "/healthz"); code != 200 {
		t.Fatalf("healthz = %d", code)
	}
	if code, body := status(t, r.addr, "/readyz"); code != 503 || !strings.Contains(body, "no destinations") {
		t.Fatalf("readyz = %d %q", code, body)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: "http://" + r.addr + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "notify_send", Arguments: map[string]any{"destination": "ops", "text": "hi"}})
	if err != nil || !res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(r.logs.String(), "no destinations") {
		t.Fatalf("degraded start should be logged: %s", r.logs)
	}
}

func TestNonexistentConfigPath_IsAlsoDegradedNotFatal(t *testing.T) {
	r := start(t, map[string]string{"NOTIFY_CONFIG": filepath.Join(t.TempDir(), "absent.yaml")})
	if code, _ := status(t, r.addr, "/readyz"); code != 503 {
		t.Fatalf("readyz = %d", code)
	}
}

func TestWithConfig_ReadyAndListsDestinations(t *testing.T) {
	r := start(t, map[string]string{"NOTIFY_CONFIG": writeConfig(t, cfgYAML), "TG_TOKEN": "111:FAKE-TOKEN"})
	if code, _ := status(t, r.addr, "/readyz"); code != 200 {
		t.Fatalf("readyz = %d", code)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: "http://" + r.addr + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "notify_list_destinations"})
	if err != nil || res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "ops") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	logs := r.logs.String()
	for _, leak := range []string{"FAKE-TOKEN", "1002223334444"} {
		if strings.Contains(logs, leak) {
			t.Fatalf("startup logs leak %q: %s", leak, logs)
		}
	}
	if !strings.Contains(logs, `"destinations":1`) {
		t.Fatalf("startup log should count destinations: %s", logs)
	}
}

func runErr(t *testing.T, env map[string]string) error {
	t.Helper()
	env["NOTIFY_ADDR"] = "127.0.0.1:0"
	return run(t.Context(), func(k string) string { return env[k] }, slog.New(slog.NewJSONHandler(io.Discard, nil)), nil)
}

func TestInvalidEnvironmentFailsStartup(t *testing.T) {
	if err := runErr(t, map[string]string{"NOTIFY_REQUEST_TIMEOUT": "never"}); err == nil || !strings.Contains(err.Error(), "NOTIFY_REQUEST_TIMEOUT") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidDestinationsFileFailsClosedWithoutEchoingIds(t *testing.T) {
	p := writeConfig(t, strings.Replace(cfgYAML, "platform: telegram", "platform: carrier-pigeon", 1))
	err := runErr(t, map[string]string{"NOTIFY_CONFIG": p, "TG_TOKEN": "111:FAKE-TOKEN"})
	if err == nil || strings.Contains(err.Error(), "1002223334444") || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingTokenFailsStartupWithoutSecrets(t *testing.T) {
	err := runErr(t, map[string]string{"NOTIFY_CONFIG": writeConfig(t, cfgYAML)})
	if err == nil || !strings.Contains(err.Error(), "TG_TOKEN") || strings.Contains(err.Error(), "1002223334444") {
		t.Fatalf("err = %v", err)
	}
}

func TestVersionStringSet(t *testing.T) {
	if version == "" {
		t.Fatal("version must have a default")
	}
}
