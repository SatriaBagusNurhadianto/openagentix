package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openagentix/openagentix/bridges/telegram/internal/config"
)

// End to end: fake Telegram + fake A2A agent, real wiring.
func TestRun_EndToEnd(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	delivered := false
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			if !delivered {
				delivered = true
				_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":1,"chat":{"id":9},"text":"ping"}}]}`)
				return
			}
			time.Sleep(5 * time.Millisecond)
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"username":"e2e_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sent = append(sent, body["text"].(string))
			_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	defer tg.Close()

	var gotAuth string
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"1","result":{"message":{"role":"ROLE_AGENT","contextId":"c1","parts":[{"text":"pong"}]}}}`)
	}))
	defer agent.Close()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	healthAddr := l.Addr().String()
	l.Close()
	var logs strings.Builder
	cfg := config.Config{BotToken: "TOKEN123", A2AURL: agent.URL, A2AToken: "AGENTTOK", APIBase: tg.URL, AgentTimeout: time.Second, AllowAll: true, HealthAddr: healthAddr}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.NewTextHandler(&logs, nil))) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no reply sent")
		}
		time.Sleep(5 * time.Millisecond)
	}
	resp, err := http.Get("http://" + healthAddr + "/healthz")
	if err != nil {
		t.Fatalf("health endpoint not serving: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop")
	}
	if sent[0] != "pong" || gotAuth != "Bearer AGENTTOK" {
		t.Fatalf("sent=%v auth=%q", sent, gotAuth)
	}
	if strings.Contains(logs.String(), "TOKEN123") || strings.Contains(logs.String(), "AGENTTOK") {
		t.Fatalf("secret in logs: %s", logs.String())
	}
}
