package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/kb/internal/chunk"
	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/kb"
	"github.com/openagentix/openagentix/services/kb/internal/store"
	"github.com/openagentix/openagentix/services/kb/internal/store/memory"
)

func newHandler(cfg Config) (http.Handler, *kb.Service) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &kb.Service{Store: memory.New(0), Embedder: embed.None{}, Chunk: chunk.Options{MaxChars: 200}, Log: log}
	return New(svc, cfg, log), svc
}

func do(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const goodBody = `{"title":"Runbook","source_uri":"https://wiki/x","text":"Restart the pod when it is stuck."}`

func TestFailClosedWhenNoTokenConfigured(t *testing.T) {
	h, _ := newHandler(Config{})
	for _, tc := range [][2]string{
		{"GET", "/v1/collections"},
		{"PUT", "/v1/collections/c/documents/d"},
		{"DELETE", "/v1/collections/c/documents/d"},
	} {
		rec := do(h, tc[0], tc[1], "anything", goodBody)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%v: code = %d, want 403", tc, rec.Code)
		}
	}
}

func TestAnonymousOptIn(t *testing.T) {
	h, _ := newHandler(Config{AllowAnonymous: true})
	if rec := do(h, "GET", "/v1/collections", "", ""); rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestTokenRequiredWhenConfigured(t *testing.T) {
	h, _ := newHandler(Config{AdminToken: "s3cret", AllowAnonymous: true}) // token wins over anonymous
	if rec := do(h, "GET", "/v1/collections", "", ""); rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("missing token: %d %v", rec.Code, rec.Header())
	}
	if rec := do(h, "GET", "/v1/collections", "wrong", ""); rec.Code != 401 {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/v1/collections", nil)
	req.Header.Set("Authorization", "Basic czNjcmV0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("basic auth: %d", rec.Code)
	}
	if rec := do(h, "GET", "/v1/collections", "s3cret", ""); rec.Code != 200 {
		t.Fatalf("right token: %d", rec.Code)
	}
}

func TestUnauthorizedDoesNotReadBodyOrLeakToken(t *testing.T) {
	h, svc := newHandler(Config{AdminToken: "s3cret"})
	rec := do(h, "PUT", "/v1/collections/c/documents/d", "nope", goodBody)
	if rec.Code != 401 || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if cols, _ := svc.Collections(t.Context()); len(cols) != 0 {
		t.Fatal("unauthenticated write stored data")
	}
}

func TestPutGetListDelete(t *testing.T) {
	h, svc := newHandler(Config{AdminToken: "t"})
	rec := do(h, "PUT", "/v1/collections/runbooks/documents/pod-restart", "t", goodBody)
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	var put struct {
		Collection, ID string
		Chunks         int
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &put)
	if put.Collection != "runbooks" || put.ID != "pod-restart" || put.Chunks != 1 {
		t.Fatalf("put resp: %s", rec.Body)
	}
	// idempotent
	if rec := do(h, "PUT", "/v1/collections/runbooks/documents/pod-restart", "t", goodBody); rec.Code != 200 {
		t.Fatalf("second put: %d", rec.Code)
	}
	v, err := svc.Get(t.Context(), "runbooks", "pod-restart")
	if err != nil || v.Title != "Runbook" || v.SourceURI != "https://wiki/x" {
		t.Fatalf("%+v %v", v, err)
	}

	rec = do(h, "GET", "/v1/collections", "t", "")
	var list struct {
		Collections []struct {
			Name      string `json:"name"`
			Documents int    `json:"documents"`
		} `json:"collections"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Collections) != 1 || list.Collections[0].Name != "runbooks" || list.Collections[0].Documents != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type %q", ct)
	}

	if rec := do(h, "DELETE", "/v1/collections/runbooks/documents/pod-restart", "t", ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/v1/collections/runbooks/documents/pod-restart", "t", ""); rec.Code != 404 {
		t.Fatalf("second delete: %d", rec.Code)
	}
}

func TestEmptyCollectionListIsEmptyArray(t *testing.T) {
	h, _ := newHandler(Config{AdminToken: "t"})
	rec := do(h, "GET", "/v1/collections", "t", "")
	if !strings.Contains(rec.Body.String(), `"collections":[]`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestValidationErrorsAre400(t *testing.T) {
	h, _ := newHandler(Config{AdminToken: "t"})
	cases := map[string]struct{ path, body string }{
		"bad collection": {"/v1/collections/bad%20name/documents/d", goodBody},
		"bad id":         {"/v1/collections/c/documents/.hidden", goodBody},
		"empty text":     {"/v1/collections/c/documents/d", `{"title":"t","text":"  "}`},
		"missing text":   {"/v1/collections/c/documents/d", `{"title":"t"}`},
		"not json":       {"/v1/collections/c/documents/d", `nope`},
		"unknown field":  {"/v1/collections/c/documents/d", `{"text":"x","admin":true}`},
		"trailing data":  {"/v1/collections/c/documents/d", `{"text":"x"} {"text":"y"}`},
		"wrong type":     {"/v1/collections/c/documents/d", `{"text":5}`},
	}
	for name, c := range cases {
		rec := do(h, "PUT", c.path, "t", c.body)
		if rec.Code != 400 {
			t.Errorf("%s: code = %d body=%s", name, rec.Code, rec.Body)
		}
		var e struct{ Error string }
		if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error == "" {
			t.Errorf("%s: no JSON error body: %s", name, rec.Body)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	h, _ := newHandler(Config{AdminToken: "t", MaxBodyBytes: 100})
	big := `{"text":"` + strings.Repeat("a", 500) + `"}`
	if rec := do(h, "PUT", "/v1/collections/c/documents/d", "t", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestDefaultBodyLimitIs4MiB(t *testing.T) {
	h, svc := newHandler(Config{AdminToken: "t"})
	svc.MaxChunksPerDoc = 1 << 20 // this test is about the body limit, not the chunk cap
	big := `{"text":"` + strings.Repeat("a", 4<<20) + `"}`
	if rec := do(h, "PUT", "/v1/collections/c/documents/d", "t", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d", rec.Code)
	}
	ok := `{"text":"` + strings.Repeat("a ", 1<<20) + `"}` // ~2 MiB
	if rec := do(h, "PUT", "/v1/collections/c/documents/d", "t", ok); rec.Code != 200 {
		t.Fatalf("2MiB body: code = %d %s", rec.Code, rec.Body)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h, _ := newHandler(Config{AdminToken: "t"})
	req := httptest.NewRequest("POST", "/v1/collections", bytes.NewReader(nil))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 405 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestInternalErrorsDoNotLeakDetails(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &kb.Service{Store: brokenStore{memory.New(0)}, Embedder: embed.None{}, Log: log}
	h := New(svc, Config{AdminToken: "t"}, log)
	rec := do(h, "GET", "/v1/collections", "t", "")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret-dsn") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestPutTooManyChunksIs413(t *testing.T) {
	h, svc := newHandler(Config{AllowAnonymous: true})
	svc.MaxChunksPerDoc = 2
	body, _ := json.Marshal(map[string]string{"text": strings.Repeat("Another sentence that fills a chunk fully ok.\n\n", 30)})
	rec := do(h, "PUT", "/v1/collections/c/documents/d", "", string(body))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "chunks") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestPutRejectsNonHTTPSourceURI(t *testing.T) {
	h, _ := newHandler(Config{AllowAnonymous: true})
	rec := do(h, "PUT", "/v1/collections/c/documents/d", "", `{"source_uri":"javascript:alert(1)","text":"x"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "source_uri") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

type canceledStore struct{ *memory.Store }

func (canceledStore) ListCollections(ctx context.Context) ([]store.CollectionInfo, error) {
	return nil, context.Canceled
}

func TestClientCancelIsNotAnInternalError(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	svc := &kb.Service{Store: canceledStore{memory.New(0)}, Embedder: embed.None{}, Chunk: chunk.Options{MaxChars: 200}, Log: log}
	h := New(svc, Config{AllowAnonymous: true}, log)
	rec := do(h, "GET", "/v1/collections", "", "")
	if rec.Code != 499 {
		t.Fatalf("code = %d, want 499 (client closed request)", rec.Code)
	}
	if strings.Contains(logs.String(), `"level":"ERROR"`) || strings.Contains(rec.Body.String(), "internal error") {
		t.Fatalf("cancellation reported as an internal error: log=%s body=%s", logs.String(), rec.Body)
	}
}
