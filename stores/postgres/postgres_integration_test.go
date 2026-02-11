//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultIntegrationDSN = "postgres://postgres:postgres@localhost:54329/vectorstore_test?sslmode=disable"

var schemaSeq atomic.Uint64

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("PGVECTOR_TEST_DSN")
	if dsn == "" {
		dsn = defaultIntegrationDSN
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		t.Skipf("skipping integration test; database unavailable at %s: %v", dsn, err)
	}

	t.Cleanup(pool.Close)
	return pool
}

func newTestStore(t *testing.T, pool *pgxpool.Pool) *PostgresVectorStore {
	t.Helper()
	seq := schemaSeq.Add(1)
	schema := fmt.Sprintf("it_%d_%d", time.Now().UnixNano(), seq)
	schema = strings.ReplaceAll(schema, "-", "_")

	store, err := NewVectorStore(pool, StoreOptions{
		Schema:          schema,
		EnsureExtension: true,
		StrictByDefault: true,
	})
	if err != nil {
		t.Fatalf("NewVectorStore: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, quoteIdent(schema)))
	})

	return store
}

func TestIntegrationEnsureCollection(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{
		Name:      "docs",
		Dimension: 3,
		Metric:    vectordata.DistanceCosine,
		Mode:      vectordata.EnsureStrict,
	})
	if err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}

	var exists bool
	err = pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2
	)`, store.opts.Schema, "docs").Scan(&exists)
	if err != nil {
		t.Fatalf("query table exists: %v", err)
	}
	if !exists {
		t.Fatalf("expected collection table to exist")
	}

	dim, err := store.readVectorDimension(ctx, "docs")
	if err != nil {
		t.Fatalf("readVectorDimension: %v", err)
	}
	if dim != 3 {
		t.Fatalf("expected dimension 3, got %d", dim)
	}
}

func TestIntegrationUpsertAndGet(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{
		Name:      "docs",
		Dimension: 2,
		Metric:    vectordata.DistanceCosine,
		Mode:      vectordata.EnsureStrict,
	})
	if err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}

	content := "first"
	err = collection.Upsert(ctx, []vectordata.Record{{
		ID:      "r1",
		Vector:  []float32{1, 0},
		Content: &content,
		Metadata: map[string]any{
			"category": "news",
			"rank":     1,
		},
	}})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rec, err := collection.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.ID != "r1" {
		t.Fatalf("expected ID r1, got %q", rec.ID)
	}
	if len(rec.Vector) != 2 || rec.Vector[0] != 1 || rec.Vector[1] != 0 {
		t.Fatalf("unexpected vector: %#v", rec.Vector)
	}
	if rec.Content == nil || *rec.Content != "first" {
		t.Fatalf("unexpected content: %#v", rec.Content)
	}
	if rec.Metadata["category"] != "news" {
		t.Fatalf("unexpected metadata category: %#v", rec.Metadata["category"])
	}

	updated := "updated"
	err = collection.Upsert(ctx, []vectordata.Record{{
		ID:      "r1",
		Vector:  []float32{0.5, 0.5},
		Content: &updated,
		Metadata: map[string]any{
			"category": "blog",
			"rank":     3,
		},
	}})
	if err != nil {
		t.Fatalf("Upsert second call: %v", err)
	}

	rec, err = collection.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get after upsert: %v", err)
	}
	if rec.Content == nil || *rec.Content != "updated" {
		t.Fatalf("content not updated: %#v", rec.Content)
	}
	if rec.Metadata["category"] != "blog" {
		t.Fatalf("metadata not updated: %#v", rec.Metadata)
	}
}

func TestIntegrationSearchByMetric(t *testing.T) {
	metrics := []vectordata.DistanceMetric{
		vectordata.DistanceCosine,
		vectordata.DistanceL2,
		vectordata.DistanceInnerProduct,
	}

	for _, metric := range metrics {
		t.Run(string(metric), func(t *testing.T) {
			pool := integrationPool(t)
			store := newTestStore(t, pool)

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{
				Name:      "search_docs",
				Dimension: 2,
				Metric:    metric,
				Mode:      vectordata.EnsureStrict,
			})
			if err != nil {
				t.Fatalf("EnsureCollection: %v", err)
			}

			err = collection.Upsert(ctx, []vectordata.Record{
				{ID: "a", Vector: []float32{1, 0}, Metadata: map[string]any{"kind": "a"}},
				{ID: "b", Vector: []float32{0.8, 0.2}, Metadata: map[string]any{"kind": "b"}},
				{ID: "c", Vector: []float32{0, 1}, Metadata: map[string]any{"kind": "c"}},
			})
			if err != nil {
				t.Fatalf("Upsert: %v", err)
			}

			results, err := collection.SearchByVector(ctx, []float32{1, 0}, 2, vectordata.SearchOptions{})
			if err != nil {
				t.Fatalf("SearchByVector: %v", err)
			}
			if len(results) != 2 {
				t.Fatalf("expected 2 results, got %d", len(results))
			}
			if results[0].Record.ID != "a" || results[1].Record.ID != "b" {
				t.Fatalf("unexpected ordering: [%s, %s]", results[0].Record.ID, results[1].Record.ID)
			}
		})
	}
}

func TestIntegrationMetadataFilter(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{
		Name:      "docs",
		Dimension: 2,
		Metric:    vectordata.DistanceCosine,
		Mode:      vectordata.EnsureStrict,
	})
	if err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}

	err = collection.Upsert(ctx, []vectordata.Record{
		{ID: "a", Vector: []float32{1, 0}, Metadata: map[string]any{"category": "news", "rank": 1}},
		{ID: "b", Vector: []float32{0.9, 0.1}, Metadata: map[string]any{"category": "news", "rank": 2}},
		{ID: "c", Vector: []float32{0, 1}, Metadata: map[string]any{"category": "other", "rank": 3}},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	filter := vectordata.And(
		vectordata.Eq(vectordata.Metadata("category"), "news"),
		vectordata.Gt(vectordata.Metadata("rank"), 1),
	)

	results, err := collection.SearchByVector(ctx, []float32{1, 0}, 10, vectordata.SearchOptions{Filter: filter})
	if err != nil {
		t.Fatalf("SearchByVector with filter: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Record.ID != "b" {
		t.Fatalf("expected result b, got %s", results[0].Record.ID)
	}

	count, err := collection.Count(ctx, filter)
	if err != nil {
		t.Fatalf("Count with filter: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}
}
