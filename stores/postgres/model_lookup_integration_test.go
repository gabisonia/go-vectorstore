//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationCollectionExistsMatchesListing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	other := newTestStore(t, pool)
	if err := store.ensureBaseSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := other.ensureBaseSchema(ctx); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	table := func(name string) string { return qualifiedTable(store.opts.Schema, name) }
	exec("CREATE TABLE " + table("legacy") + " (v vector(2))")
	exec("CREATE TABLE " + table("legacy_half") + " (v halfvec(2))")
	exec("CREATE TABLE " + table("partitioned") + " (id int, v vector(2)) PARTITION BY RANGE(id)")
	exec("CREATE TABLE " + table(`quoted.name"x`) + " (v vector(2))")
	exec("CREATE TABLE " + table("ordinary") + " (id int)")
	exec("CREATE TABLE " + table("dropped_vector") + " (id int, v vector(2))")
	exec("ALTER TABLE " + table("dropped_vector") + " DROP COLUMN v")
	exec("CREATE VIEW " + table("vector_view") + " AS SELECT '[1,0]'::vector(2) AS v")
	exec("CREATE TABLE " + qualifiedTable(other.opts.Schema, "other_schema") + " (v vector(2))")
	// A same-named ordinary local relation must not inherit another schema's vector.
	exec("CREATE TABLE " + qualifiedTable(other.opts.Schema, "ordinary") + " (v vector(2))")
	expected := map[string]bool{"legacy": true, "partitioned": true, `quoted.name"x`: true, "ordinary": false, "dropped_vector": false, "vector_view": false, "other_schema": false, "missing": false, "scalar_model": false}
	check := func() {
		t.Helper()
		names, err := store.ListCollectionNames(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range expected {
			got, err := store.CollectionExists(ctx, name)
			if err != nil || got != want {
				t.Fatalf("CollectionExists(%q)=%v, want=%v, error=%v", name, got, want, err)
			}
			if slices.Contains(names, name) != got {
				t.Fatalf("listing and lookup disagree for %q: names=%v, exists=%v", name, names, got)
			}
		}
	}
	expected["legacy_half"] = true
	// Legacy collections are discoverable before the registry has been created.
	check()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}
	if _, err := store.EnsureModelCollection(ctx, "scalar_model", d, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	expected["scalar_model"] = true
	check()
	// A stale registry entry does not make a dropped relation or replacement view a collection.
	exec("DROP TABLE " + table("scalar_model"))
	expected["scalar_model"] = false
	check()
	exec("CREATE VIEW " + table("scalar_model") + " AS SELECT 1 AS id")
	check()
	if err := store.DeleteCollection(ctx, `quoted.name"x`); err != nil {
		t.Fatal(err)
	}
	expected[`quoted.name"x`] = false
	check()
	if err := store.DeleteCollection(ctx, `quoted.name"x`); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	for _, name := range []string{"", modelRegistry, strings.Repeat("x", 64), "bad\x00name"} {
		if _, err := store.CollectionExists(ctx, name); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("invalid name %q: %v", name, err)
		}
	}
	canceled, cancelRequest := context.WithCancel(ctx)
	cancelRequest()
	if _, err := store.CollectionExists(canceled, "legacy"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	missing, err := NewVectorStore(pool, StoreOptions{Schema: store.opts.Schema + "_missing"})
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := missing.CollectionExists(ctx, "legacy"); err != nil || exists {
		t.Fatalf("missing schema: exists=%v, error=%v", exists, err)
	}
	// Quoted schema identifiers must survive regclass lookup as well as table names.
	schema := store.opts.Schema + `.odd"schema`
	exec("CREATE SCHEMA " + quoteIdent(schema))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE") })
	quoted, err := NewVectorStore(pool, StoreOptions{Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := quoted.EnsureModelCollection(ctx, `model.table"x`, d, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	if exists, err := quoted.CollectionExists(ctx, `model.table"x`); err != nil || !exists {
		t.Fatalf("quoted schema/model: exists=%v, error=%v", exists, err)
	}
}

type lookupQueryTrace struct {
	mu        sync.Mutex
	capture   bool
	rowCounts []int64
}

func (*lookupQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (tr *lookupQueryTrace) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.capture {
		tr.rowCounts = append(tr.rowCounts, data.CommandTag.RowsAffected())
	}
}

func TestIntegrationCollectionExistsDoesNotEnumerateCollections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(integrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	trace := new(lookupQueryTrace)
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := newTestStore(t, pool)
	if err := store.ensureBaseSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c", "target"} {
		if _, err := pool.Exec(ctx, "CREATE TABLE "+qualifiedTable(store.opts.Schema, name)+" (v vector(2))"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.EnsureModelCollection(ctx, "scalar", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		queries int
		want    bool
	}{{"target", 1, true}, {"scalar", 2, true}, {"missing", 1, false}} {
		t.Run(tt.name, func(t *testing.T) {
			trace.mu.Lock()
			trace.rowCounts = nil
			trace.capture = true
			trace.mu.Unlock()
			exists, err := store.CollectionExists(ctx, tt.name)
			trace.mu.Lock()
			trace.capture = false
			counts := slices.Clone(trace.rowCounts)
			trace.mu.Unlock()
			if err != nil || exists != tt.want {
				t.Fatalf("exists=%v, error=%v", exists, err)
			}
			if len(counts) > tt.queries {
				t.Fatalf("queries=%d, maximum %d", len(counts), tt.queries)
			}
			for _, count := range counts {
				if count > 1 {
					t.Fatalf("lookup enumerated %d rows: %v", count, counts)
				}
			}
		})
	}
}
