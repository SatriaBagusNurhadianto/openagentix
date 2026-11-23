package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openagentix/openagentix/services/kb/internal/chunk"
	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/kb"
	"github.com/openagentix/openagentix/services/kb/internal/store"
	"github.com/openagentix/openagentix/services/kb/internal/store/memory"
)

func graphHandler(t *testing.T, on bool) (http.Handler, *kb.Service) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &kb.Service{Store: memory.New(0), Embedder: embed.None{}, Chunk: chunk.Options{MaxChars: 200}, Log: log}
	if on {
		svc.Graph = &kb.GraphOptions{WindowChars: 2400, PromptVersion: "v1"}
	}
	return New(svc, Config{AdminToken: "tok"}, log), svc
}

func TestGraphStatsEndpoint(t *testing.T) {
	h, svc := graphHandler(t, true)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := svc.Put(ctx, "kg", id, kb.DocInput{Text: "Secret-body of " + id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Put(ctx, "other", "x", kb.DocInput{Text: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	j1, _, _ := svc.Store.ClaimJob(ctx, time.Minute)
	if err := svc.Store.ApplyExtraction(ctx, store.ExtractionResult{Job: j1.JobKey, Model: "m", PromptVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
	j2, _, _ := svc.Store.ClaimJob(ctx, time.Minute)
	if err := svc.Store.FailJob(ctx, j2.JobKey, "http_429", 0, true); err != nil {
		t.Fatal(err)
	}

	rec := do(h, "GET", "/v1/collections/"+j1.Collection+"/graph/stats", "tok", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	var got struct {
		Collection string `json:"collection"`
		Entities   int    `json:"entities"`
		Relations  int    `json:"relations"`
		Jobs       struct{ Pending, Done, Failed int }
		LastError  string `json:"last_error_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Collection != "kg" && got.Collection != "other" {
		t.Fatalf("collection = %q", got.Collection)
	}
	rec = do(h, "GET", "/v1/collections/kg/graph/stats", "tok", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Collection != "kg" || got.Jobs.Pending+got.Jobs.Done+got.Jobs.Failed != 3 {
		t.Fatalf("kg stats = %+v (%s)", got, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "Secret-body") {
		t.Fatal("stats leak content")
	}

	rec = do(h, "GET", "/v1/graph/stats", "tok", "")
	var all struct {
		Collections []struct {
			Collection string `json:"collection"`
		} `json:"collections"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &all) != nil || len(all.Collections) != 2 {
		t.Fatalf("all: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/v1/collections/no%2Fgood/graph/stats", "tok", ""); rec.Code != 400 && rec.Code != 404 {
		t.Fatalf("bad collection: %d", rec.Code)
	}
	if rec := do(h, "GET", "/v1/collections/kg/graph/stats", "wrong", ""); rec.Code != 401 {
		t.Fatalf("auth: %d", rec.Code)
	}
}

func TestGraphStatsReportsLastErrorCodeOnly(t *testing.T) {
	h, svc := graphHandler(t, true)
	ctx := context.Background()
	if _, err := svc.Put(ctx, "kg", "a", kb.DocInput{Text: "text"}); err != nil {
		t.Fatal(err)
	}
	j, _, _ := svc.Store.ClaimJob(ctx, time.Minute)
	_ = svc.Store.FailJob(ctx, j.JobKey, "malformed_output", 0, true)
	rec := do(h, "GET", "/v1/collections/kg/graph/stats", "tok", "")
	if !strings.Contains(rec.Body.String(), `"last_error_code":"malformed_output"`) {
		t.Fatalf("%s", rec.Body)
	}
}

func TestGraphStatsAbsentWhenGraphDisabled(t *testing.T) {
	h, _ := graphHandler(t, false)
	for _, p := range []string{"/v1/graph/stats", "/v1/collections/kg/graph/stats"} {
		if rec := do(h, "GET", p, "tok", ""); rec.Code != 404 {
			t.Errorf("%s: code = %d, want 404", p, rec.Code)
		}
	}
}
