package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func envFn(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// start runs the service and returns its base URL and a stop func that waits for exit.
func start(t *testing.T, env map[string]string, logs *bytes.Buffer) (string, func() error) {
	t.Helper()
	if env["KB_ADDR"] == "" {
		env["KB_ADDR"] = "127.0.0.1:0"
	}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan string, 1)
	errc := make(chan error, 1)
	go func() { errc <- run(ctx, envFn(env), log, func(a string) { addr <- a }) }()
	select {
	case a := <-addr:
		return "http://" + a, func() error { cancel(); return <-errc }
	case err := <-errc:
		cancel()
		t.Fatalf("run exited early: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("timeout waiting for listener")
	}
	return "", nil
}

func TestMemoryModeServesAndStops(t *testing.T) {
	var logs bytes.Buffer
	base, stop := start(t, map[string]string{"KB_ADMIN_TOKEN": "tok-abc-123"}, &logs)
	resp, err := http.Get(base + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, resp)
	}
	resp.Body.Close()
	resp, _ = http.Get(base + "/readyz")
	if resp.StatusCode != 200 {
		t.Fatalf("readyz %d", resp.StatusCode)
	}
	resp.Body.Close()
	if err := stop(); err != nil {
		t.Fatalf("run returned %v", err)
	}
	if strings.Contains(logs.String(), "tok-abc-123") {
		t.Fatalf("startup logs leak the admin token: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"store":"memory"`) {
		t.Fatalf("config summary not logged: %s", logs.String())
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	err := run(context.Background(), envFn(map[string]string{"KB_STORE": "oracle"}), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), nil)
	if err == nil || !strings.Contains(err.Error(), "KB_STORE") {
		t.Fatalf("err = %v", err)
	}
}

func TestPostgresUnreachableFailsStartupWithoutLeakingPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := run(ctx, envFn(map[string]string{
		"KB_STORE": "postgres", "KB_DATABASE_URL": "postgres://user:pw-hunter2@127.0.0.1:1/db?connect_timeout=1", "KB_ADDR": "127.0.0.1:0",
	}), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), nil)
	if err == nil || strings.Contains(err.Error(), "pw-hunter2") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmbedderWiredIntoIngestAndSearch(t *testing.T) {
	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer emb-key" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var in struct{ Input []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		var data []map[string]any
		for i := range in.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer fake.Close()

	var logs bytes.Buffer
	base, stop := start(t, map[string]string{
		"KB_ADMIN_TOKEN": "t", "KB_EMBEDDINGS_URL": fake.URL + "/v1", "KB_EMBEDDINGS_MODEL": "m",
		"KB_EMBEDDINGS_DIM": "3", "KB_EMBEDDINGS_API_KEY": "emb-key",
	}, &logs)
	req, _ := http.NewRequest("PUT", base+"/v1/collections/c/documents/d", strings.NewReader(`{"text":"hello embeddings"}`))
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ingest: %v %v", err, resp)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("embedder calls after ingest = %d", calls.Load())
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "emb-key") {
		t.Fatalf("logs leak embeddings key: %s", logs.String())
	}
}

func TestVersionStringSet(t *testing.T) {
	if version == "" {
		t.Fatal(fmt.Sprint("version must default to a non-empty value"))
	}
}

// TestPostgresEndToEnd runs the whole binary logic against a real PostgreSQL
// with pgvector and a fake embedder; it needs KB_TEST_DATABASE_URL.
func TestPostgresEndToEnd(t *testing.T) {
	dsn := os.Getenv("KB_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KB_TEST_DATABASE_URL not set")
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		var data []map[string]any
		for i, s := range in.Input {
			v := []float32{0, 1, 0}
			if strings.Contains(s, "kubernetes") {
				v = []float32{1, 0, 0}
			}
			data = append(data, map[string]any{"index": i, "embedding": v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer fake.Close()

	schema := fmt.Sprintf("kbtest_e2e_%d", time.Now().UnixNano())
	var logs bytes.Buffer
	base, stop := start(t, map[string]string{
		"KB_ADMIN_TOKEN": "t", "KB_STORE": "postgres", "KB_DATABASE_URL": dsn, "KB_DB_SCHEMA": schema,
		"KB_EMBEDDINGS_URL": fake.URL, "KB_EMBEDDINGS_MODEL": "m", "KB_EMBEDDINGS_DIM": "3",
	}, &logs)
	defer func() {
		pool, err := pgxpool.New(context.Background(), dsn)
		if err == nil {
			_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
			pool.Close()
		}
	}()

	req, _ := http.NewRequest("PUT", base+"/v1/collections/ops/documents/k8s", strings.NewReader(`{"title":"K8s","text":"kubernetes schedules pods"}`))
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ingest: %v %v", err, resp)
	}
	resp.Body.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: base + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "kb_search", Arguments: map[string]any{"query": "kubernetes"}})
	sess.Close()
	if err != nil || res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "K8s") {
		t.Fatalf("search: %v %+v", err, res)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}
