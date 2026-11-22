package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentix/openagentix/services/kb/internal/store"
	"github.com/openagentix/openagentix/services/kb/internal/store/storetest"
)

const envURL = "KB_TEST_DATABASE_URL"

func testURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv(envURL)
	if u == "" {
		t.Skipf("%s not set; skipping PostgreSQL integration test", envURL)
	}
	return u
}

func randomSchema() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "kbtest_" + hex.EncodeToString(b)
}

// open returns a store in a fresh schema that is dropped at test end.
func open(t *testing.T, dim int) (*Store, string) {
	t.Helper()
	return openAt(t, testURL(t), dim)
}

func openAt(t *testing.T, url string, dim int) (*Store, string) {
	t.Helper()
	schema := randomSchema()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := Open(ctx, Options{URL: url, Schema: schema, VectorDim: dim})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
		pool, err := pgxpool.New(context.Background(), url)
		if err != nil {
			return
		}
		defer pool.Close()
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	return s, schema
}

func TestConformanceLexical(t *testing.T) {
	testURL(t)
	storetest.Run(t, storetest.Factory{New: func(t *testing.T) store.Store { s, _ := open(t, 0); return s }})
}

func TestConformanceHybrid(t *testing.T) {
	testURL(t)
	storetest.Run(t, storetest.Factory{New: func(t *testing.T) store.Store { s, _ := open(t, 3); return s }, Vectors: true, Dim: 3})
}

func TestMigrationsAreIdempotent(t *testing.T) {
	url := testURL(t)
	schema := randomSchema()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		s, err := Open(ctx, Options{URL: url, Schema: schema, VectorDim: 3})
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		if i == 0 {
			if err := s.UpsertDocument(ctx, store.Document{Collection: "c", ID: "a", Text: "t"}, []store.Chunk{{Text: "keepme"}}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
		}
		s.Close()
	}
	s, err := Open(ctx, Options{URL: url, Schema: schema, VectorDim: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		s.Close()
		p, _ := pgxpool.New(ctx, url)
		_, _ = p.Exec(ctx, `DROP SCHEMA "`+schema+`" CASCADE`)
		p.Close()
	}()
	hits, err := s.Search(ctx, store.Query{Text: "keepme", TopK: 3})
	if err != nil || len(hits) != 1 {
		t.Fatalf("data lost across re-open: %+v %v", hits, err)
	}
}

func TestConcurrentOpenSameSchema(t *testing.T) {
	url := testURL(t)
	schema := randomSchema()
	ctx := context.Background()
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			s, err := Open(ctx, Options{URL: url, Schema: schema, VectorDim: 3})
			if err == nil {
				s.Close()
			}
			errs <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent open: %v", err)
		}
	}
	p, _ := pgxpool.New(ctx, url)
	_, _ = p.Exec(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	p.Close()
}

func TestVectorDimensionMismatchFailsClearly(t *testing.T) {
	url := testURL(t)
	_, schema := open(t, 3)
	_, err := Open(context.Background(), Options{URL: url, Schema: schema, VectorDim: 5})
	if err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("err = %v", err)
	}
}

func TestLexicalOnlyOpenDoesNotRequireVectorExtension(t *testing.T) {
	url := testURL(t)
	schema := randomSchema()
	s, err := Open(context.Background(), Options{URL: url, Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	p, _ := pgxpool.New(context.Background(), url)
	defer p.Close()
	var n int
	_ = p.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.columns WHERE table_schema=$1 AND column_name='embedding'`, schema).Scan(&n)
	if n != 0 {
		t.Fatal("embedding column created although vectors are disabled")
	}
	_, _ = p.Exec(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`)
}

func TestMissingVectorExtensionFailsStartupClearly(t *testing.T) {
	url := os.Getenv("KB_TEST_DATABASE_URL_NOVECTOR")
	if url == "" {
		t.Skip("KB_TEST_DATABASE_URL_NOVECTOR not set")
	}
	_, err := Open(context.Background(), Options{URL: url, Schema: randomSchema(), VectorDim: 3})
	if err == nil || !strings.Contains(err.Error(), "pgvector") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidSchemaNameRejected(t *testing.T) {
	for _, s := range []string{"", "Bad-Name", `x"; drop schema public; --`, strings.Repeat("a", 64)} {
		if _, err := Open(context.Background(), Options{URL: "postgres://localhost:1/x", Schema: s}); err == nil || !strings.Contains(err.Error(), "schema") {
			t.Errorf("schema %q: err = %v", s, err)
		}
	}
}

func TestOpenUnreachableFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := Open(ctx, Options{URL: "postgres://u:p@127.0.0.1:1/x?connect_timeout=1", Schema: "kb"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestWebSearchQuery(t *testing.T) {
	cases := map[string]string{
		"kubernetes ingress":   "kubernetes or ingress",
		"  spaced   out  ":     "spaced or out",
		"single":               "single",
		`"exact phrase" other`: `"exact phrase" other`,
		"alpha or beta":        "alpha or beta",
		"foo -bar":             "foo -bar",
		"":                     "",
	}
	for in, want := range cases {
		if got := webSearchQuery(in); got != want {
			t.Errorf("webSearchQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVectorLiteral(t *testing.T) {
	if got := vectorLiteral([]float32{1, 0.5, -2}); got != "[1,0.5,-2]" {
		t.Fatalf("got %q", got)
	}
}

func dropSchema(t *testing.T, url, schema string) {
	t.Helper()
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		return
	}
	defer p.Close()
	_, _ = p.Exec(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
}

func TestTwoSchemasShareOnePgvectorExtension(t *testing.T) {
	url := testURL(t)
	ctx := context.Background()
	a, b := randomSchema(), randomSchema()
	t.Cleanup(func() { dropSchema(t, url, a); dropSchema(t, url, b) })
	for _, schema := range []string{a, b} {
		s, err := Open(ctx, Options{URL: url, Schema: schema, VectorDim: 3})
		if err != nil {
			t.Fatalf("Open(%s): %v", schema, err)
		}
		doc := store.Document{Collection: "c", ID: "d", Text: "t"}
		if err := s.UpsertDocument(ctx, doc, []store.Chunk{{Text: "vec", Embedding: []float32{1, 0, 0}}}); err != nil {
			s.Close()
			t.Fatalf("upsert in %s: %v", schema, err)
		}
		hits, err := s.Search(ctx, store.Query{Embedding: []float32{1, 0, 0}, TopK: 3})
		s.Close()
		if err != nil || len(hits) != 1 {
			t.Fatalf("search in %s: %+v %v", schema, hits, err)
		}
	}
	// The extension must not live inside a KB schema (dropping that schema would take it away).
	p, _ := pgxpool.New(ctx, url)
	defer p.Close()
	var ns string
	if err := p.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'vector'`).Scan(&ns); err != nil {
		t.Fatal(err)
	}
	if ns == a || ns == b {
		t.Fatalf("vector extension installed inside KB schema %q", ns)
	}
}

// tempDatabase creates a scratch database (so extension placement can be
// manipulated without disturbing tests running in parallel) and returns its URL.
func tempDatabase(t *testing.T) string {
	t.Helper()
	base := testURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := "kbscratch_" + strings.TrimPrefix(randomSchema(), "kbtest_")
	if _, err := admin.Exec(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
		admin.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func TestUsesPgvectorAlreadyInstalledInAnotherSchema(t *testing.T) {
	dbURL := tempDatabase(t)
	ctx := context.Background()
	p, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Exec(ctx, `CREATE SCHEMA ext; CREATE EXTENSION vector WITH SCHEMA ext`); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, Options{URL: dbURL, Schema: "kb", VectorDim: 3})
	if err != nil {
		t.Fatalf("Open with the extension in schema ext: %v", err)
	}
	defer s.Close()
	if err := s.UpsertDocument(ctx, store.Document{Collection: "c", ID: "d", Text: "t"}, []store.Chunk{{Text: "x", Embedding: []float32{0, 1, 0}}}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(ctx, store.Query{Embedding: []float32{0, 1, 0}, TopK: 3})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

// seedFilterScenario fills a big collection whose vectors are all close to the
// query (angles 0..60 degrees from it) and a second one that is far from it
// (70..85 degrees) but still positively similar. Vectors are spread along an
// arc, which keeps the HNSW graph connected as real embeddings do. Both
// collections are large enough that the planner scans the HNSW index for a
// collection-filtered query, as it does in production.
func seedFilterScenario(t *testing.T, s *Store, bigChunks, smallChunks int) {
	t.Helper()
	ctx := context.Background()
	put := func(coll string, n int, from, to float64) {
		if _, err := s.pool.Exec(ctx, `INSERT INTO `+s.schema+`.documents (collection, id, text)
			SELECT $1::text, 'd' || (i / 200)::text, 't' FROM generate_series(0, $2::int - 1) i GROUP BY i / 200`, coll, n); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO `+s.schema+`.chunks (collection, doc_id, idx, text, embedding)
			SELECT $1::text, 'd' || (i / 200)::text, i % 200, $1::text || ' chunk ' || i::text,
			       ('[' || cos(radians($3::float8 + ($4::float8 - $3::float8) * i / $2::float8)) || ',' || sin(radians($3::float8 + ($4::float8 - $3::float8) * i / $2::float8)) || ',' || ((i % 7) * 0.001)::text || ']')::`+s.vecSchema+`.vector
			FROM generate_series(0, $2::int - 1) i`, coll, n, from, to); err != nil {
			t.Fatal(err)
		}
	}
	put("big", bigChunks, 0, 60)
	put("small", smallChunks, 70, 85)
	if _, err := s.pool.Exec(ctx, `ANALYZE `+s.schema+`.chunks`); err != nil {
		t.Fatal(err)
	}
}

func TestFilteredVectorSearchNotUnderReturned(t *testing.T) {
	s, _ := open(t, 3)
	seedFilterScenario(t, s, 15000, 10000)
	ctx := context.Background()
	hits, err := s.Search(ctx, store.Query{Embedding: []float32{1, 0, 0}, Collection: "small", TopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 5 {
		t.Fatalf("got %d hits from the small collection, want 5: %+v", len(hits), hits)
	}
	for _, h := range hits {
		if h.Collection != "small" {
			t.Fatalf("hit from wrong collection: %+v", h)
		}
	}
}

func TestUnavailableSettingDoesNotPoisonTransaction(t *testing.T) {
	s, _ := open(t, 3)
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if trySet(ctx, tx, `SET LOCAL hnsw.no_such_setting = 1`) {
		t.Fatal("unknown setting reported as applied")
	}
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("transaction unusable after a failed SET: %v", err)
	}
}
