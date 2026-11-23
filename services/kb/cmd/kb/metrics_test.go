package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMetricsListenerOffByDefault(t *testing.T) {
	called := false
	onMetricsListen = func(string) { called = true }
	defer func() { onMetricsListen = nil }()
	base, stop := start(t, map[string]string{"KB_ADMIN_TOKEN": "tok"}, io.Discard)
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("API listener serves /metrics: %d", resp.StatusCode)
	}
	if err := stop(); err != nil || called {
		t.Fatalf("err=%v metrics listener started=%v", err, called)
	}
}

func TestMetricsListenerEndToEndAndPrivacy(t *testing.T) {
	addr := make(chan string, 1)
	onMetricsListen = func(a string) { addr <- a }
	defer func() { onMetricsListen = nil }()
	var logs bytes.Buffer
	base, stop := start(t, map[string]string{"KB_ADMIN_TOKEN": "tok-canary-token", "KB_METRICS_ADDR": "127.0.0.1:0"}, &logs)
	var maddr string
	select {
	case maddr = <-addr:
	case <-time.After(5 * time.Second):
		t.Fatal("metrics listener did not start")
	}
	mbase := "http://" + maddr

	do := func(method, url, body string) (int, string) {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-canary-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, b := do("PUT", base+"/v1/collections/canary-collection/documents/canary-doc-id",
		`{"title":"canary-title","source_uri":"https://canary.example/src","text":"canary-document-text about zebras"}`); code != 200 && code != 201 {
		t.Fatalf("ingest %d %s", code, b)
	}
	// An MCP search goes through the same service.
	code, _ := do("POST", base+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kb_search","arguments":{"query":"canary-query-text zebras"}}}`)
	_ = code

	code, out := do("GET", mbase+"/metrics", "")
	if code != 200 {
		t.Fatalf("metrics %d", code)
	}
	for _, want := range []string{
		`openagentix_kb_http_requests_total{route="/v1/collections/{collection}/documents/{id}",method="PUT",status_class="2xx"} 1`,
		`openagentix_kb_documents_ingested_total{outcome="ok"} 1`,
		`openagentix_kb_build_info{version="dev"} 1`,
		"go_goroutines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, canary := range []string{"canary", "zebras", "tok-"} {
		if strings.Contains(out, canary) {
			t.Errorf("forbidden content %q in /metrics:\n%s", canary, out)
		}
	}
	if code, _ := do("GET", mbase+"/v1/collections", ""); code != 404 {
		t.Errorf("metrics listener serves the API: %d", code)
	}
	if code, _ := do("GET", mbase+"/mcp", ""); code != 404 {
		t.Errorf("metrics listener serves MCP: %d", code)
	}
	if code, _ := do("GET", base+"/metrics", ""); code != 404 {
		t.Errorf("API listener serves /metrics: %d", code)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"canary-document-text", "canary-query-text", "tok-canary-token"} {
		if strings.Contains(logs.String(), canary) {
			t.Errorf("forbidden content %q in logs", canary)
		}
	}
}
