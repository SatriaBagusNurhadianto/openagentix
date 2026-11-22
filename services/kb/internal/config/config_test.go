package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.Store != "memory" || c.DBSchema != "kb" || c.MaxBodyBytes != 4<<20 ||
		c.ChunkMaxChars != 1200 || c.ChunkOverlap != 150 || c.RequestTimeout != 30*time.Second ||
		c.ShutdownGrace != 25*time.Second || c.DocMaxChars != 50000 || c.EmbeddingsDim != 0 || c.AdminToken != "" || c.AdminAllowAnonymous {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"KB_ADDR": ":9000", "KB_ADMIN_TOKEN": "tok", "KB_MAX_BODY_BYTES": "1024", "KB_CHUNK_MAX_CHARS": "500",
		"KB_CHUNK_OVERLAP": "50", "KB_REQUEST_TIMEOUT": "5s", "KB_SHUTDOWN_GRACE": "1s", "KB_DOC_MAX_CHARS": "100",
		"KB_ADMIN_ALLOW_ANONYMOUS": "true", "KB_LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":9000" || c.AdminToken != "tok" || c.MaxBodyBytes != 1024 || c.ChunkMaxChars != 500 || c.ChunkOverlap != 50 ||
		c.RequestTimeout != 5*time.Second || c.ShutdownGrace != time.Second || c.DocMaxChars != 100 || !c.AdminAllowAnonymous || c.LogLevel != "debug" {
		t.Fatalf("%+v", c)
	}
}

func TestInvalidValues(t *testing.T) {
	cases := map[string]map[string]string{
		"KB_STORE":                 {"KB_STORE": "mongo"},
		"KB_MAX_BODY_BYTES":        {"KB_MAX_BODY_BYTES": "-1"},
		"KB_CHUNK_MAX_CHARS":       {"KB_CHUNK_MAX_CHARS": "abc"},
		"KB_CHUNK_OVERLAP":         {"KB_CHUNK_OVERLAP": "5000"},
		"KB_REQUEST_TIMEOUT":       {"KB_REQUEST_TIMEOUT": "soon"},
		"KB_SHUTDOWN_GRACE":        {"KB_SHUTDOWN_GRACE": "-1s"},
		"KB_ADMIN_ALLOW_ANONYMOUS": {"KB_ADMIN_ALLOW_ANONYMOUS": "maybe"},
		"KB_DOC_MAX_CHARS":         {"KB_DOC_MAX_CHARS": "0"},
		"KB_LOG_LEVEL":             {"KB_LOG_LEVEL": "loud"},
		"KB_DB_SCHEMA":             {"KB_DB_SCHEMA": "bad-name;drop"},
	}
	for name, m := range cases {
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestPostgresRequiresURL(t *testing.T) {
	if _, err := Load(env(map[string]string{"KB_STORE": "postgres"})); err == nil || !strings.Contains(err.Error(), "KB_DATABASE_URL") {
		t.Fatalf("err = %v", err)
	}
	c, err := Load(env(map[string]string{"KB_STORE": "postgres", "KB_DATABASE_URL": "postgres://u:p@h/db", "KB_DB_SCHEMA": "kb_prod"}))
	if err != nil || c.DatabaseURL == "" || c.DBSchema != "kb_prod" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestEmbeddingsConfig(t *testing.T) {
	c, err := Load(env(map[string]string{
		"KB_EMBEDDINGS_URL": "http://gw/v1", "KB_EMBEDDINGS_MODEL": "m", "KB_EMBEDDINGS_DIM": "768", "KB_EMBEDDINGS_API_KEY": "k",
	}))
	if err != nil || !c.EmbeddingsEnabled() || c.EmbeddingsDim != 768 || c.EmbeddingsAPIKey != "k" {
		t.Fatalf("%+v %v", c, err)
	}
	if c2, _ := Load(env(nil)); c2.EmbeddingsEnabled() {
		t.Fatal("embeddings should be off by default")
	}
	for name, m := range map[string]map[string]string{
		"no model":       {"KB_EMBEDDINGS_URL": "http://gw", "KB_EMBEDDINGS_DIM": "4"},
		"no dim":         {"KB_EMBEDDINGS_URL": "http://gw", "KB_EMBEDDINGS_MODEL": "m"},
		"bad dim":        {"KB_EMBEDDINGS_URL": "http://gw", "KB_EMBEDDINGS_MODEL": "m", "KB_EMBEDDINGS_DIM": "x"},
		"model sans url": {"KB_EMBEDDINGS_MODEL": "m"},
		"dim too big":    {"KB_EMBEDDINGS_URL": "http://gw", "KB_EMBEDDINGS_MODEL": "m", "KB_EMBEDDINGS_DIM": "100000"},
	} {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLogAttrsNeverContainSecrets(t *testing.T) {
	c, _ := Load(env(map[string]string{
		"KB_ADMIN_TOKEN": "supersecret-token", "KB_EMBEDDINGS_API_KEY": "sk-secret", "KB_STORE": "postgres",
		"KB_DATABASE_URL": "postgres://user:dbpass@host/db", "KB_EMBEDDINGS_URL": "http://gw", "KB_EMBEDDINGS_MODEL": "m", "KB_EMBEDDINGS_DIM": "3",
	}))
	out := ""
	for _, a := range c.LogAttrs() {
		out += " " + toString(a)
	}
	for _, secret := range []string{"supersecret-token", "sk-secret", "dbpass"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log attrs leak %q: %s", secret, out)
		}
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		return strings.TrimSpace(strings.ReplaceAll(fmtAny(x), "\n", " "))
	}
}

func fmtAny(v any) string { return fmt.Sprint(v) }

func TestMCPStatelessDefaultAndOverride(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil || !c.MCPStateless {
		t.Fatalf("default should be stateless: %+v %v", c, err)
	}
	c, err = Load(env(map[string]string{"KB_MCP_STATELESS": "false"}))
	if err != nil || c.MCPStateless {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Load(env(map[string]string{"KB_MCP_STATELESS": "yes"})); err == nil || !strings.Contains(err.Error(), "KB_MCP_STATELESS") {
		t.Fatalf("err = %v", err)
	}
}

func TestQueryEmbedTimeoutSetting(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil || c.QueryEmbedTimeout != 3*time.Second {
		t.Fatalf("default: %v %v", c.QueryEmbedTimeout, err)
	}
	c, err = Load(env(map[string]string{"KB_QUERY_EMBED_TIMEOUT": "500ms"}))
	if err != nil || c.QueryEmbedTimeout != 500*time.Millisecond {
		t.Fatalf("override: %v %v", c.QueryEmbedTimeout, err)
	}
	if _, err := Load(env(map[string]string{"KB_QUERY_EMBED_TIMEOUT": "0"})); err == nil {
		t.Fatal("zero must be rejected")
	}
}

func TestIngestLimitsSettings(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil || c.IngestTimeout != 120*time.Second || c.MaxChunksPerDoc != 2000 {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c, err = Load(env(map[string]string{"KB_INGEST_TIMEOUT": "5m", "KB_MAX_CHUNKS_PER_DOC": "10"}))
	if err != nil || c.IngestTimeout != 5*time.Minute || c.MaxChunksPerDoc != 10 {
		t.Fatalf("override: %+v %v", c, err)
	}
	for _, m := range []map[string]string{{"KB_INGEST_TIMEOUT": "x"}, {"KB_MAX_CHUNKS_PER_DOC": "0"}} {
		if _, err := Load(env(m)); err == nil {
			t.Fatalf("%v accepted", m)
		}
	}
}

func TestEmbeddingsDimCappedAtHNSWLimit(t *testing.T) {
	base := map[string]string{"KB_EMBEDDINGS_URL": "http://x/v1", "KB_EMBEDDINGS_MODEL": "m"}
	with := func(dim string) map[string]string {
		m := map[string]string{"KB_EMBEDDINGS_DIM": dim}
		for k, v := range base {
			m[k] = v
		}
		return m
	}
	if _, err := Load(env(with("2000"))); err != nil {
		t.Fatalf("2000 must be accepted: %v", err)
	}
	_, err := Load(env(with("2001")))
	if err == nil || !strings.Contains(err.Error(), "2000") {
		t.Fatalf("2001: err = %v, want an error naming the 2000 limit", err)
	}
}

func TestChunkMaxCharsCapped(t *testing.T) {
	if _, err := Load(env(map[string]string{"KB_CHUNK_MAX_CHARS": "100000"})); err != nil {
		t.Fatalf("100000 must be accepted: %v", err)
	}
	if _, err := Load(env(map[string]string{"KB_CHUNK_MAX_CHARS": "100001"})); err == nil {
		t.Fatal("100001 accepted")
	}
}
