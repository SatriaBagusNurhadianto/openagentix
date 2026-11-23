package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openagentix/openagentix/services/kb/internal/chunk"
	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/kb"
	"github.com/openagentix/openagentix/services/kb/internal/obs"
	"github.com/openagentix/openagentix/services/kb/internal/store/memory"
)

func TestToolCallMetrics(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := obs.New("test")
	svc := &kb.Service{Store: memory.New(0), Embedder: embed.None{}, Chunk: chunk.Options{MaxChars: 200}, DocMaxChars: 80, Log: log}
	mux := http.NewServeMux()
	mux.Handle("/mcp", New(svc, log, Options{Stateless: true, Version: "test", Metrics: m}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	_, _ = svc.Put(ctx, "docs", "d1", kb.DocInput{Text: "canary-doc-text lives here"})
	call := func(tool string, args map[string]any) {
		if _, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args}); err != nil {
			t.Fatal(err)
		}
	}
	call("kb_search", map[string]any{"query": "canary-query-text"})
	call("kb_search", map[string]any{"query": "   "})
	call("kb_get_document", map[string]any{"document_id": "d1"})
	call("kb_get_document", map[string]any{"document_id": "nope-canary-id"})
	call("kb_list_collections", map[string]any{})

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		`openagentix_kb_mcp_tool_calls_total{tool="kb_search",outcome="ok"} 1`,
		`openagentix_kb_mcp_tool_calls_total{tool="kb_search",outcome="invalid"} 1`,
		`openagentix_kb_mcp_tool_calls_total{tool="kb_get_document",outcome="ok"} 1`,
		`openagentix_kb_mcp_tool_calls_total{tool="kb_get_document",outcome="not_found"} 1`,
		`openagentix_kb_mcp_tool_calls_total{tool="kb_list_collections",outcome="ok"} 1`,
		`openagentix_kb_mcp_tool_call_duration_seconds_count{tool="kb_search"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, canary := range []string{"canary", "docs"} {
		if strings.Contains(out, canary) {
			t.Errorf("%q leaked into metrics", canary)
		}
	}
}
