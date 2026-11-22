package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type req struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// fake serves deterministic 3-dim vectors: [len(text), 1, 0].
func fake(t *testing.T, calls *atomic.Int32, reorder bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in req
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("decode: %v", err)
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i, s := range in.Input {
			data = append(data, item{i, []float32{float32(len(s)), 1, 0}})
		}
		if reorder {
			for i, j := 0, len(data)-1; i < j; i, j = i+1, j-1 {
				data[i], data[j] = data[j], data[i]
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func TestNoneIsDisabled(t *testing.T) {
	var e Embedder = None{}
	if e.Dimensions() != 0 {
		t.Fatal("none must report zero dimensions")
	}
	v, err := e.Embed(context.Background(), []string{"x"})
	if err != nil || v != nil {
		t.Fatalf("v=%v err=%v", v, err)
	}
}

func TestOpenAIEmbedsWithAuthAndModel(t *testing.T) {
	var calls atomic.Int32
	var gotAuth, gotPath string
	var gotBody req
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2,3]}]}`)
	}))
	defer srv.Close()
	e, err := NewOpenAI(OpenAIConfig{URL: srv.URL + "/v1", Model: "m1", APIKey: "sekret", Dim: 3})
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0]) != 3 || out[0][2] != 3 {
		t.Fatalf("out = %v", out)
	}
	if gotAuth != "Bearer sekret" || gotPath != "/v1/embeddings" || gotBody.Model != "m1" || gotBody.Input[0] != "hi" {
		t.Fatalf("auth=%q path=%q body=%+v", gotAuth, gotPath, gotBody)
	}
	if e.Dimensions() != 3 {
		t.Fatal("dimensions")
	}
}

func TestOpenAIFullEndpointURLUsedAsIs(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2,3]}]}`)
	}))
	defer srv.Close()
	e, _ := NewOpenAI(OpenAIConfig{URL: srv.URL + "/custom/embeddings", Model: "m", Dim: 3})
	if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/custom/embeddings" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestOpenAIBatchesAndPreservesOrder(t *testing.T) {
	var calls atomic.Int32
	srv := fake(t, &calls, true)
	defer srv.Close()
	e, _ := NewOpenAI(OpenAIConfig{URL: srv.URL, Model: "m", Dim: 3, BatchSize: 4})
	texts := make([]string, 10)
	for i := range texts {
		texts[i] = strings.Repeat("a", i+1)
	}
	out, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	for i, v := range out {
		if v[0] != float32(i+1) {
			t.Fatalf("order broken at %d: %v", i, v)
		}
	}
}

func TestOpenAIRetriesOnceOn5xxAnd429(t *testing.T) {
	for _, code := range []int{429, 503} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				http.Error(w, "busy", code)
				return
			}
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2,3]}]}`)
		}))
		e, _ := NewOpenAI(OpenAIConfig{URL: srv.URL, Model: "m", Dim: 3, RetryDelay: time.Millisecond})
		if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
			t.Fatalf("code %d: %v", code, err)
		}
		if calls.Load() != 2 {
			t.Fatalf("code %d: calls = %d", code, calls.Load())
		}
		srv.Close()
	}
}

func TestOpenAIGivesUpAfterOneRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "down", 500)
	}))
	defer srv.Close()
	e, _ := NewOpenAI(OpenAIConfig{URL: srv.URL, Model: "m", Dim: 3, RetryDelay: time.Millisecond, APIKey: "sekret"})
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	if strings.Contains(err.Error(), "sekret") {
		t.Fatal("error leaks the API key")
	}
}

func TestOpenAIDoesNotRetry4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad", 400)
	}))
	defer srv.Close()
	e, _ := NewOpenAI(OpenAIConfig{URL: srv.URL, Model: "m", Dim: 3, RetryDelay: time.Millisecond})
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestOpenAIRejectsBadResponses(t *testing.T) {
	cases := map[string]string{
		"wrong dim":   `{"data":[{"index":0,"embedding":[1,2]}]}`,
		"wrong count": `{"data":[]}`,
		"bad index":   `{"data":[{"index":5,"embedding":[1,2,3]}]}`,
		"not json":    `<html>`,
	}
	for name, body := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		e, _ := NewOpenAI(OpenAIConfig{URL: srv.URL, Model: "m", Dim: 3})
		if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
			t.Errorf("%s: expected error", name)
		}
		srv.Close()
	}
}

func TestOpenAIResponseSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,2,3]}],"pad":"`+strings.Repeat("x", 2048)+`"}`)
	}))
	defer srv.Close()
	e, _ := NewOpenAI(OpenAIConfig{URL: srv.URL, Model: "m", Dim: 3, MaxResponseBytes: 512})
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAITimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // lets the server notice the client hanging up
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	e, _ := NewOpenAI(OpenAIConfig{URL: srv.URL, Model: "m", Dim: 3, Timeout: 50 * time.Millisecond, RetryDelay: time.Millisecond})
	start := time.Now()
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout not honoured")
	}
}

func TestOpenAIEmptyInputMakesNoRequest(t *testing.T) {
	var calls atomic.Int32
	srv := fake(t, &calls, false)
	defer srv.Close()
	e, _ := NewOpenAI(OpenAIConfig{URL: srv.URL, Model: "m", Dim: 3})
	out, err := e.Embed(context.Background(), nil)
	if err != nil || len(out) != 0 || calls.Load() != 0 {
		t.Fatalf("out=%v err=%v calls=%d", out, err, calls.Load())
	}
}

func TestNewOpenAIValidatesConfig(t *testing.T) {
	for _, c := range []OpenAIConfig{
		{Model: "m", Dim: 3},
		{URL: "http://x", Dim: 3},
		{URL: "http://x", Model: "m"},
		{URL: "ftp://x", Model: "m", Dim: 3},
	} {
		if _, err := NewOpenAI(c); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func TestOpenAIEmbedQueryDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	e, err := NewOpenAI(OpenAIConfig{URL: srv.URL, Model: "m", Dim: 3, RetryDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	q, ok := any(e).(QueryEmbedder)
	if !ok {
		t.Fatal("OpenAI does not implement QueryEmbedder")
	}
	if _, err := q.EmbedQuery(context.Background(), "hi"); err == nil {
		t.Fatal("expected error")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on the query path)", n)
	}
}
