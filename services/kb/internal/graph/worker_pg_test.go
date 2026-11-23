package graph

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentix/openagentix/services/kb/internal/store/postgres"
)

// pgRig is a rig on a real PostgreSQL (with pgvector, dimension 3) in a
// throwaway schema. It needs KB_TEST_DATABASE_URL like the store tests.
func pgRig(t *testing.T, content func(int, map[string]any) string, tweak func(*Options)) *rig {
	t.Helper()
	url := os.Getenv("KB_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KB_TEST_DATABASE_URL not set; skipping PostgreSQL worker test")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "kbtest_w_" + hex.EncodeToString(b)
	st, err := postgres.Open(t.Context(), postgres.Options{URL: url, Schema: schema, VectorDim: 3, Graph: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		if p, err := pgxpool.New(context.Background(), url); err == nil {
			_, _ = p.Exec(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
			p.Close()
		}
	})
	return newRigOn(t, st, content, tweak)
}

func TestWorkerOnPostgresHappyPathReingestAndReplace(t *testing.T) {
	r := pgRig(t, goodAnswer, nil)
	r.w.embedder = &fakeEmbedder{}
	r.put("arch", chunkA, chunkB)
	r.drain()
	st := r.stats()
	if st.Entities != 2 || st.Relations != 1 || st.JobsDone != 1 || st.Mentions < 2 {
		t.Fatalf("stats = %+v", st)
	}
	hits, err := r.st.FindEntitiesByEmbedding(t.Context(), coll, []float32{1, 0, 0}, 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("name embeddings not stored: %+v %v", hits, err)
	}
	if n := r.put("arch", chunkA, chunkB); n != 0 || r.gw.calls() != 1 {
		t.Fatalf("re-ingest: jobs=%d calls=%d", n, r.gw.calls())
	}
	if n := r.put("arch", "Frankfurt is a city."); n != 1 {
		t.Fatalf("replace: jobs=%d", n)
	}
	if st := r.stats(); st.Entities != 0 || st.Relations != 0 || st.Mentions != 0 {
		t.Fatalf("old rows survive: %+v", st)
	}
}

func TestWorkerOnPostgresRestartMidJobLeavesNoDuplicates(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	r := pgRig(t, nil, func(o *Options) { o.Lease = 200 * time.Millisecond })
	r.gw.content = func(n int, req map[string]any) string {
		if n == 1 {
			entered <- struct{}{}
			<-release
		}
		return goodAnswer(n, req)
	}
	r.put("d", chunkA)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.w.Step(ctx) }()
	<-entered
	cancel()
	close(release)
	<-done
	time.Sleep(300 * time.Millisecond)
	if worked, _ := r.w.Step(t.Context()); !worked {
		t.Fatal("expired lease not reclaimed")
	}
	if st := r.stats(); st.Entities != 2 || st.Relations != 1 || st.JobsDone != 1 {
		t.Fatalf("stats = %+v", st)
	}
}
