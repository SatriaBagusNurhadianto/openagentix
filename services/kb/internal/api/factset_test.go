package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/openagentix/openagentix/services/kb/internal/chunk"
	"github.com/openagentix/openagentix/services/kb/internal/embed"
	"github.com/openagentix/openagentix/services/kb/internal/kb"
	"github.com/openagentix/openagentix/services/kb/internal/store/memory"
)

const factsetBody = `{
  "source_uri": "https://example.test/catalogue",
  "entities": [
    {"name": "Basalt", "type": "team", "aliases": ["basalt squad"]},
    {"name": "Priya Venkat", "type": "person", "description": "Engineering manager"},
    {"name": "Orion", "type": "service"}
  ],
  "relations": [
    {"source": "Basalt", "predicate": "escalates_to", "target": "Priya Venkat", "note": "Escalations go to Priya Venkat"},
    {"source": "Basalt", "predicate": "owns", "target": "Orion"}
  ]
}`

const fsPath = "/v1/collections/kg/graph/factsets/org-catalogue"

func TestFactSetLifecycle(t *testing.T) {
	h, _ := graphHandler(t, true)
	rec := do(h, "PUT", fsPath, "tok", factsetBody)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	var put struct {
		Collection, ID      string
		Entities, Relations int
		Changed             bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &put); err != nil || put.Collection != "kg" || put.ID != "org-catalogue" || put.Entities != 3 || put.Relations != 2 || !put.Changed {
		t.Fatalf("put response: %s", rec.Body)
	}
	rec = do(h, "PUT", fsPath, "tok", factsetBody)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"changed":false`) {
		t.Fatalf("second put: %d %s", rec.Code, rec.Body)
	}

	rec = do(h, "GET", fsPath, "tok", "")
	var got struct {
		SourceURI string `json:"source_uri"`
		Entities  []struct{ Name, Type string }
		Relations []struct{ Source, Predicate, Target, Note string }
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.SourceURI != "https://example.test/catalogue" || len(got.Entities) != 3 || got.Relations[0].Note != "Escalations go to Priya Venkat" {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}

	rec = do(h, "GET", "/v1/collections/kg/graph/factsets", "tok", "")
	var list struct {
		Collection string
		Factsets   []struct {
			ID                  string
			SourceURI           string `json:"source_uri"`
			Entities, Relations int
			UpdatedAt           string `json:"updated_at"`
		}
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil || list.Collection != "kg" || len(list.Factsets) != 1 ||
		list.Factsets[0].Entities != 3 || list.Factsets[0].Relations != 2 || list.Factsets[0].UpdatedAt == "" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/v1/collections/other/graph/factsets", "tok", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"factsets":[]`) {
		t.Fatalf("empty list must be an array: %d %s", rec.Code, rec.Body)
	}

	rec = do(h, "GET", "/v1/collections/kg/graph/stats", "tok", "")
	if !strings.Contains(rec.Body.String(), `"manual":{"entities":3,"factsets":1,"relations":2}`) {
		t.Fatalf("stats: %s", rec.Body)
	}

	if rec := do(h, "DELETE", fsPath, "tok", ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := do(h, "DELETE", fsPath, "tok", ""); rec.Code != 404 || !strings.Contains(rec.Body.String(), "fact set not found") {
		t.Fatalf("second delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", fsPath, "tok", ""); rec.Code != 404 {
		t.Fatalf("get after delete: %d", rec.Code)
	}
}

func TestFactSetAuthAndAbsentWithoutGraph(t *testing.T) {
	h, _ := graphHandler(t, true)
	for _, c := range [][2]string{{"PUT", fsPath}, {"GET", fsPath}, {"DELETE", fsPath}, {"GET", "/v1/collections/kg/graph/factsets"}} {
		if rec := do(h, c[0], c[1], "wrong", factsetBody); rec.Code != 401 {
			t.Errorf("%v with a wrong token: %d", c, rec.Code)
		}
		if rec := do(h, c[0], c[1], "", factsetBody); rec.Code != 401 {
			t.Errorf("%v without a token: %d", c, rec.Code)
		}
	}
	closed, _ := newHandler(Config{})
	if rec := do(closed, "PUT", fsPath, "anything", factsetBody); rec.Code != 404 && rec.Code != 403 {
		t.Fatalf("no token configured: %d", rec.Code)
	}
	off, _ := graphHandler(t, false)
	for _, c := range [][2]string{{"PUT", fsPath}, {"GET", fsPath}, {"DELETE", fsPath}, {"GET", "/v1/collections/kg/graph/factsets"}} {
		if rec := do(off, c[0], c[1], "tok", factsetBody); rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%v with the graph off: %d (the route must not exist)", c, rec.Code)
		}
	}
}

func TestFactSetValidationErrors(t *testing.T) {
	h, _ := graphHandler(t, true)
	cases := map[string]struct{ path, body, want string }{
		"bad type":       {fsPath, `{"entities":[{"name":"A","type":"gadget"}],"relations":[]}`, "entities[0]: type"},
		"undeclared end": {fsPath, `{"entities":[{"name":"A","type":"team"}],"relations":[{"source":"A","predicate":"owns","target":"B"}]}`, "relations[0]: target is not an entity declared"},
		"bad source uri": {fsPath, `{"source_uri":"file:///etc/passwd","entities":[],"relations":[]}`, "source_uri"},
		"bad id":         {"/v1/collections/kg/graph/factsets/.hidden", factsetBody, "id"},
		"bad collection": {"/v1/collections/no%20good/graph/factsets/x", factsetBody, "collection"},
		"unknown field":  {fsPath, `{"entities":[],"relations":[],"admin":true}`, "unknown field"},
		"not json":       {fsPath, `nope`, "malformed JSON"},
		"wrong type":     {fsPath, `{"entities":"x"}`, "wrong type"},
		"trailing data":  {fsPath, `{"entities":[],"relations":[]} {}`, "single JSON object"},
	}
	for name, c := range cases {
		rec := do(h, "PUT", c.path, "tok", c.body)
		var e struct{ Error string }
		if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &e) != nil || !strings.Contains(e.Error, c.want) {
			t.Errorf("%s: %d %s (want %q)", name, rec.Code, rec.Body, c.want)
		}
	}
	if rec := do(h, "GET", "/v1/collections/kg/graph/factsets", "tok", ""); !strings.Contains(rec.Body.String(), `"factsets":[]`) {
		t.Fatalf("a rejected fact set was stored: %s", rec.Body)
	}
}

func TestFactSetSizeLimits(t *testing.T) {
	h, _ := graphHandler(t, true)
	var sb strings.Builder
	sb.WriteString(`{"entities":[`)
	for i := 0; i <= 2000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"name":"S%d","type":"service"}`, i)
	}
	sb.WriteString(`],"relations":[]}`)
	if rec := do(h, "PUT", fsPath, "tok", sb.String()); rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "2000") {
		t.Fatalf("too many entities: %d %s", rec.Code, rec.Body)
	}
	small := New(&kb.Service{Store: memory.New(0), Embedder: embed.None{}, Chunk: chunk.Options{MaxChars: 200},
		Graph: &kb.GraphOptions{WindowChars: 2400, PromptVersion: "v1"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Config{AdminToken: "tok", MaxBodyBytes: 100}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if rec := do(small, "PUT", fsPath, "tok", factsetBody); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit: %d", rec.Code)
	}
}

func TestFactSetLogsNeverContainCatalogueContent(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	svc := &kb.Service{Store: memory.New(0), Embedder: embed.None{}, Chunk: chunk.Options{MaxChars: 200}, Log: log,
		Graph: &kb.GraphOptions{WindowChars: 2400, PromptVersion: "v1"}}
	h := New(svc, Config{AdminToken: "tok"}, log)
	do(h, "PUT", fsPath, "tok", factsetBody)
	do(h, "PUT", fsPath, "tok", factsetBody)
	do(h, "PUT", fsPath, "tok", `{"entities":[{"name":"Priya Venkat","type":"gadget"}],"relations":[]}`)
	do(h, "DELETE", fsPath, "tok", "")
	out := logs.String()
	if !strings.Contains(out, "fact set replaced") || !strings.Contains(out, "org-catalogue") {
		t.Fatalf("nothing logged: %q", out)
	}
	for _, secret := range []string{"Priya", "Venkat", "Basalt", "Orion", "escalates_to", "Escalations", "basalt squad", "Engineering manager", "example.test"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log contains %q: %s", secret, out)
		}
	}
}
