//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestIntegrationMetadataNumericPrecisionAndOverflow(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "numbers", Dimension: 2})
	if err != nil {
		t.Fatal(err)
	}
	records := []vectordata.Record{
		{ID: "large", Vector: []float32{1, 0}, Metadata: map[string]any{"value": int64(9007199254740993)}},
		{ID: "decimal", Vector: []float32{1, 0}, Metadata: map[string]any{"value": json.Number("0.1234567890123456789")}},
		{ID: "unsigned", Vector: []float32{1, 0}, Metadata: map[string]any{"value": uint64(18446744073709551615)}},
		{ID: "large-exponent", Vector: []float32{1, 0}, Metadata: map[string]any{"value": "1e999"}},
		{ID: "overflow", Vector: []float32{1, 0}, Metadata: map[string]any{"value": "1e999999999"}},
		{ID: "underflow", Vector: []float32{1, 0}, Metadata: map[string]any{"value": "1e-999999999"}},
		{ID: "text", Vector: []float32{1, 0}, Metadata: map[string]any{"value": "not a number"}},
		{ID: "special", Vector: []float32{1, 0}, Metadata: map[string]any{"value": "NaN"}},
		{ID: "null", Vector: []float32{1, 0}, Metadata: map[string]any{"value": nil}},
		{ID: "missing", Vector: []float32{1, 0}},
	}
	if err := collection.Upsert(ctx, records); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		filter vectordata.Filter
		want   int64
	}{
		{"large integer", vectordata.And(vectordata.Eq(vectordata.Column("id"), "large"), vectordata.Gt(vectordata.Metadata("value"), int64(9007199254740992))), 1},
		{"large argument", vectordata.And(vectordata.Eq(vectordata.Column("id"), "large"), vectordata.Lt(vectordata.Metadata("value"), int64(9007199254740994))), 1},
		{"decimal", vectordata.And(vectordata.Eq(vectordata.Column("id"), "decimal"), vectordata.Gt(vectordata.Metadata("value"), json.Number("0.1234567890123456788"))), 1},
		{"unsigned equality boundary", vectordata.And(vectordata.Eq(vectordata.Column("id"), "unsigned"), vectordata.Lt(vectordata.Metadata("value"), uint64(18446744073709551615))), 0},
		{"mixed values", vectordata.Gt(vectordata.Metadata("value"), 0), 4},
		{"negative comparison", vectordata.Lt(vectordata.Metadata("value"), 0), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			count, err := collection.Count(ctx, tt.filter)
			if err != nil || count != tt.want {
				t.Fatalf("Count: got %d, want %d, error %v", count, tt.want, err)
			}
			results, err := collection.SearchByVector(ctx, []float32{1, 0}, 20, vectordata.SearchOptions{Filter: tt.filter})
			if err != nil || int64(len(results)) != tt.want {
				t.Fatalf("SearchByVector: got %d results, want %d, error %v", len(results), tt.want, err)
			}
		})
	}
	for _, tt := range []struct{ id, want string }{{"large", "9007199254740993"}, {"decimal", "0.1234567890123456789"}, {"unsigned", "18446744073709551615"}} {
		record, err := collection.Get(ctx, tt.id)
		if err != nil {
			t.Fatal(err)
		}
		if record.Metadata["value"] != json.Number(tt.want) {
			t.Fatalf("Get(%q) lost precision: %#v", tt.id, record.Metadata)
		}
		if err := collection.Upsert(ctx, []vectordata.Record{record}); err != nil {
			t.Fatal(err)
		}
		results, err := collection.SearchByVector(ctx, []float32{1, 0}, 1, vectordata.SearchOptions{Filter: vectordata.Eq(vectordata.Column("id"), tt.id)})
		if err != nil || len(results) != 1 || results[0].Record.Metadata["value"] != json.Number(tt.want) {
			t.Fatalf("search after round trip lost precision: %#v, %v", results, err)
		}
	}
}

const (
	integrationPostgresUser     = "postgres"
	integrationPostgresPassword = "postgres"
	integrationPostgresDatabase = "vectorstore_test"
)

var (
	schemaSeq            atomic.Uint64
	integrationDSN       string
	integrationContainer testcontainers.Container
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dsn := strings.TrimSpace(os.Getenv("PGVECTOR_TEST_DSN"))
	if dsn == "" {
		container, generatedDSN, err := startPgVectorContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start integration container: %v\n", err)
			os.Exit(1)
		}
		integrationContainer = container
		integrationDSN = generatedDSN
	} else {
		integrationDSN = dsn
	}

	exitCode := m.Run()

	if integrationContainer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		if err := integrationContainer.Terminate(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to terminate integration container: %v\n", err)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}

	os.Exit(exitCode)
}

func startPgVectorContainer(ctx context.Context) (testcontainers.Container, string, error) {
	request := testcontainers.ContainerRequest{
		Image:        "pgvector/pgvector:pg16",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     integrationPostgresUser,
			"POSTGRES_PASSWORD": integrationPostgresPassword,
			"POSTGRES_DB":       integrationPostgresDatabase,
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(2 * time.Minute),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: request,
		Started:          true,
	})
	if err != nil {
		return nil, "", fmt.Errorf("start pgvector container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(context.Background())
		return nil, "", fmt.Errorf("resolve container host: %w", err)
	}
	mappedPort, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		_ = container.Terminate(context.Background())
		return nil, "", fmt.Errorf("resolve container port: %w", err)
	}

	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		integrationPostgresUser,
		integrationPostgresPassword,
		host,
		mappedPort.Port(),
		integrationPostgresDatabase,
	)

	if err := waitForDatabase(ctx, dsn); err != nil {
		_ = container.Terminate(context.Background())
		return nil, "", err
	}

	return container, dsn, nil
}

func waitForDatabase(parent context.Context, dsn string) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()

	for {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			return fmt.Errorf("parse integration DSN: %w", err)
		}

		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
			pingErr := pool.Ping(pingCtx)
			pingCancel()
			pool.Close()
			if pingErr == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			if err != nil {
				return fmt.Errorf("connect integration database: %w", err)
			}
			return fmt.Errorf("wait for integration database: %w", ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := strings.TrimSpace(integrationDSN)
	if dsn == "" {
		t.Fatal("integration DSN is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
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
	// Arrange
	pool := integrationPool(t)
	store := newTestStore(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Act
	_, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{
		Name:      "docs",
		Dimension: 3,
		Metric:    vectordata.DistanceCosine,
		Mode:      vectordata.EnsureStrict,
	})

	// Assert
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
	// Arrange
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
	firstUpsertErr := collection.Upsert(ctx, []vectordata.Record{{
		ID:      "r1",
		Vector:  []float32{1, 0},
		Content: &content,
		Metadata: map[string]any{
			"category": "news",
			"rank":     1,
		},
	}})

	// Act
	rec, getErr := collection.Get(ctx, "r1")

	// Assert
	if firstUpsertErr != nil {
		t.Fatalf("Upsert: %v", firstUpsertErr)
	}
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
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
	secondUpsertErr := collection.Upsert(ctx, []vectordata.Record{{
		ID:      "r1",
		Vector:  []float32{0.5, 0.5},
		Content: &updated,
		Metadata: map[string]any{
			"category": "blog",
			"rank":     3,
		},
	}})

	// Act
	updatedRec, updatedGetErr := collection.Get(ctx, "r1")

	// Assert
	if secondUpsertErr != nil {
		t.Fatalf("Upsert second call: %v", secondUpsertErr)
	}
	if updatedGetErr != nil {
		t.Fatalf("Get after upsert: %v", updatedGetErr)
	}
	if updatedRec.Content == nil || *updatedRec.Content != "updated" {
		t.Fatalf("content not updated: %#v", updatedRec.Content)
	}
	if updatedRec.Metadata["category"] != "blog" {
		t.Fatalf("metadata not updated: %#v", updatedRec.Metadata)
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
			// Arrange
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

			// Act
			results, err := collection.SearchByVector(ctx, []float32{1, 0}, 2, vectordata.SearchOptions{})

			// Assert
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
	// Arrange
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

	// Act
	results, searchErr := collection.SearchByVector(ctx, []float32{1, 0}, 10, vectordata.SearchOptions{Filter: filter})
	count, countErr := collection.Count(ctx, filter)

	// Assert
	if searchErr != nil {
		t.Fatalf("SearchByVector with filter: %v", searchErr)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Record.ID != "b" {
		t.Fatalf("expected result b, got %s", results[0].Record.ID)
	}
	if countErr != nil {
		t.Fatalf("Count with filter: %v", countErr)
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}
}

func TestIntegrationMetadataContainmentPreservesMatches(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "docs", Dimension: 2})
	if err != nil {
		t.Fatal(err)
	}
	for id, metadata := range map[string]map[string]any{
		"exact":    {"nested": map[string]any{"value": []any{1, 2}}},
		"superset": {"nested": map[string]any{"value": []any{1, 2, 3}}},
		"array":    {"nested": []any{map[string]any{"value": 1}}},
		"null":     {"nested": map[string]any{"value": nil}},
		"missing":  {},
	} {
		if err := collection.Insert(ctx, []vectordata.Record{{ID: id, Vector: []float32{1, 0}, Metadata: metadata}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, filter := range []vectordata.Filter{
		vectordata.Eq(vectordata.Metadata("nested", "value"), []any{1, 2}),
		vectordata.In(vectordata.Metadata("nested", "value"), []any{1, 2}, nil),
		vectordata.Eq(vectordata.Metadata("nested", "0", "value"), 1),
		vectordata.Not(vectordata.Eq(vectordata.Metadata("nested", "value"), nil)),
		vectordata.Not(vectordata.Or(vectordata.Eq(vectordata.Metadata("nested", "value"), []any{1, 2}), vectordata.Eq(vectordata.Metadata("nested", "value"), nil))),
	} {
		baselineCfg := collection.(*PostgresCollection).filterConfig()
		baselineCfg.UseMetadataContainment = false
		predicate, args, _, err := vectordata.CompileFilterSQL(filter, baselineCfg, 1)
		if err != nil {
			t.Fatal(err)
		}
		var want int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+qualifiedTable(store.opts.Schema, "docs")+" WHERE "+predicate, args...).Scan(&want); err != nil {
			t.Fatal(err)
		}
		got, err := collection.Count(ctx, filter)
		if err != nil || got != want {
			t.Fatalf("filter %#v: got %d, want %d, error %v", filter, got, want, err)
		}
	}
}

func TestIntegrationMetadataFilterUsesGIN(t *testing.T) {
	pool := integrationPool(t)
	for _, pathOps := range []bool{false, true} {
		t.Run(fmt.Sprint(pathOps), func(t *testing.T) {
			store := newTestStore(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "docs", Dimension: 2})
			if err != nil {
				t.Fatal(err)
			}
			table := qualifiedTable(store.opts.Schema, "docs")
			if _, err := pool.Exec(ctx, "INSERT INTO "+table+" (id, vector, metadata) SELECT n::text, '[1,0]'::vector, jsonb_build_object('category', CASE WHEN n % 1000 = 0 THEN 'news' ELSE 'other' END) FROM generate_series(1,20000) n"); err != nil {
				t.Fatal(err)
			}
			if err := collection.EnsureIndexes(ctx, vectordata.IndexOptions{Metadata: &vectordata.MetadataIndexOptions{Name: "metadata_filter", UsePathOps: pathOps}}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "ANALYZE "+table); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
				t.Fatal(err)
			}
			for _, filter := range []vectordata.Filter{vectordata.Eq(vectordata.Metadata("category"), "news"), vectordata.In(vectordata.Metadata("category"), "news", "absent")} {
				predicate, args, _, err := vectordata.CompileFilterSQL(filter, collection.(*PostgresCollection).filterConfig(), 1)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := tx.Query(ctx, "EXPLAIN SELECT id FROM "+table+" WHERE "+predicate, args...)
				if err != nil {
					t.Fatal(err)
				}
				var plan strings.Builder
				for rows.Next() {
					var line string
					if err := rows.Scan(&line); err != nil {
						t.Fatal(err)
					}
					plan.WriteString(line)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				if !strings.Contains(plan.String(), "metadata_filter") {
					t.Fatalf("GIN index not usable: %s", plan.String())
				}
			}
		})
	}
}

func TestIntegrationGetNotFoundAndDelete(t *testing.T) {
	// Arrange
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
	if err := collection.Upsert(ctx, []vectordata.Record{{ID: "r1", Vector: []float32{1, 0}}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Act
	deleted, deleteErr := collection.Delete(ctx, []string{"r1", "missing"})
	_, getErr := collection.Get(ctx, "r1")

	// Assert
	if deleteErr != nil {
		t.Fatalf("Delete: %v", deleteErr)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted row, got %d", deleted)
	}
	if !errors.Is(getErr, vectordata.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", getErr)
	}
}

func TestIntegrationInsertDuplicateFailsWithoutOverwrite(t *testing.T) {
	// Arrange
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
	if err := collection.Insert(ctx, []vectordata.Record{{ID: "r1", Vector: []float32{1, 0}}}); err != nil {
		t.Fatalf("Insert first record: %v", err)
	}

	// Act
	err = collection.Insert(ctx, []vectordata.Record{{ID: "r1", Vector: []float32{0, 1}}})
	rec, getErr := collection.Get(ctx, "r1")

	// Assert
	if err == nil {
		t.Fatal("expected duplicate Insert to fail")
	}
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if rec.Vector[0] != 1 || rec.Vector[1] != 0 {
		t.Fatalf("duplicate Insert overwrote record: %#v", rec.Vector)
	}
}

func TestIntegrationSearchProjectionAndThreshold(t *testing.T) {
	// Arrange
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
	content := "alpha"
	if err := collection.Upsert(ctx, []vectordata.Record{
		{ID: "a", Vector: []float32{1, 0}, Content: &content, Metadata: map[string]any{"category": "near"}},
		{ID: "b", Vector: []float32{0, 1}, Metadata: map[string]any{"category": "far"}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	threshold := 0.05
	projection := vectordata.Projection{IncludeVector: true}

	// Act
	results, err := collection.SearchByVector(ctx, []float32{1, 0}, 10, vectordata.SearchOptions{
		Projection: &projection,
		Threshold:  &threshold,
	})

	// Assert
	if err != nil {
		t.Fatalf("SearchByVector: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 thresholded result, got %d", len(results))
	}
	rec := results[0].Record
	if rec.ID != "a" {
		t.Fatalf("expected result a, got %s", rec.ID)
	}
	if len(rec.Vector) != 2 || rec.Vector[0] != 1 || rec.Vector[1] != 0 {
		t.Fatalf("expected vector projection, got %#v", rec.Vector)
	}
	if rec.Metadata != nil {
		t.Fatalf("metadata should not be projected: %#v", rec.Metadata)
	}
	if rec.Content != nil {
		t.Fatalf("content should not be projected: %#v", rec.Content)
	}
}

func TestIntegrationMultiBatchInsertRollsBackOnLaterFailure(t *testing.T) {
	// Arrange
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

	records := make([]vectordata.Record, 0, maxRowsPerStatement+1)
	for i := 0; i < maxRowsPerStatement; i++ {
		records = append(records, vectordata.Record{
			ID:     fmt.Sprintf("r%d", i),
			Vector: []float32{1, 0},
		})
	}
	records = append(records, vectordata.Record{ID: "r0", Vector: []float32{0, 1}})

	// Act
	insertErr := collection.Insert(ctx, records)
	count, countErr := collection.Count(ctx, nil)

	// Assert
	if insertErr == nil {
		t.Fatal("expected duplicate record in later batch to fail")
	}
	if countErr != nil {
		t.Fatalf("Count: %v", countErr)
	}
	if count != 0 {
		t.Fatalf("expected transaction rollback to leave 0 records, got %d", count)
	}
}

func TestIntegrationMultiBatchValidationFailureRollsBack(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "docs", Dimension: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, upsert := range []bool{false, true} {
		records := benchmarkRecords(maxRowsPerStatement+1, 2)
		records[len(records)-1].Metadata = map[string]any{"bad": func() {}}
		if upsert {
			err = collection.Upsert(ctx, records)
		} else {
			err = collection.Insert(ctx, records)
		}
		if !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("expected ErrInvalidRecord, got %v", err)
		}
		count, err := collection.Count(ctx, nil)
		if err != nil || count != 0 {
			t.Fatalf("partial write after validation failure: count %d, error %v", count, err)
		}
	}
}

func TestIntegrationSchemaMismatchAndAutoMigrate(t *testing.T) {
	// Arrange
	pool := integrationPool(t)
	store := newTestStore(t, pool)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := store.ensureBaseSchema(ctx); err != nil {
		t.Fatalf("ensureBaseSchema: %v", err)
	}
	_, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			%s text PRIMARY KEY,
			%s vector(2) NOT NULL
		)
	`, qualifiedTable(store.opts.Schema, "legacy_docs"), quoteIdent(idColumn), quoteIdent(vectorColumn)))
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}

	// Act
	_, strictErr := store.EnsureCollection(ctx, vectordata.CollectionSpec{
		Name:      "legacy_docs",
		Dimension: 2,
		Metric:    vectordata.DistanceCosine,
		Mode:      vectordata.EnsureStrict,
	})
	collection, migrateErr := store.EnsureCollection(ctx, vectordata.CollectionSpec{
		Name:      "legacy_docs",
		Dimension: 2,
		Metric:    vectordata.DistanceCosine,
		Mode:      vectordata.EnsureAutoMigrate,
	})
	upsertErr := error(nil)
	if collection != nil {
		upsertErr = collection.Upsert(ctx, []vectordata.Record{{ID: "r1", Vector: []float32{1, 0}}})
	}

	// Assert
	if !errors.Is(strictErr, vectordata.ErrSchemaMismatch) {
		t.Fatalf("expected strict schema mismatch, got %v", strictErr)
	}
	if migrateErr != nil {
		t.Fatalf("auto-migrate EnsureCollection: %v", migrateErr)
	}
	if upsertErr != nil {
		t.Fatalf("Upsert after auto-migrate: %v", upsertErr)
	}
}

func TestIntegrationEnsureIndexesCreatesIndexes(t *testing.T) {
	// Arrange
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

	// Act
	err = collection.EnsureIndexes(ctx, vectordata.IndexOptions{
		Vector:   &vectordata.VectorIndexOptions{Name: "docs_vec_hnsw", Method: vectordata.IndexMethodHNSW},
		Metadata: &vectordata.MetadataIndexOptions{Name: "docs_metadata_gin"},
	})

	// Assert
	if err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	if err := collection.EnsureIndexes(ctx, vectordata.IndexOptions{
		Vector:   &vectordata.VectorIndexOptions{Name: "docs_vec_hnsw", Method: vectordata.IndexMethodHNSW},
		Metadata: &vectordata.MetadataIndexOptions{Name: "docs_metadata_gin"},
	}); err != nil {
		t.Fatalf("idempotent EnsureIndexes: %v", err)
	}
	for _, indexName := range []string{"docs_vec_hnsw", "docs_metadata_gin"} {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_indexes
				WHERE schemaname = $1 AND tablename = $2 AND indexname = $3
			)
		`, store.opts.Schema, "docs", indexName).Scan(&exists)
		if err != nil {
			t.Fatalf("check index %s: %v", indexName, err)
		}
		if !exists {
			t.Fatalf("expected index %s to exist", indexName)
		}
	}
}

func TestIntegrationLongCollectionIndexNames(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: strings.Repeat("d", 63), Dimension: 2})
	if err != nil {
		t.Fatal(err)
	}
	opts := vectordata.IndexOptions{Vector: &vectordata.VectorIndexOptions{}, Metadata: &vectordata.MetadataIndexOptions{}}
	for i := 0; i < 2; i++ {
		if err := collection.EnsureIndexes(ctx, opts); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntegrationEnsureIndexesRejectsConflicts(t *testing.T) {
	pool := integrationPool(t)
	for _, tt := range []struct {
		name, definition string
		metadata         bool
	}{
		{"metric", "USING hnsw (vector vector_l2_ops)", false},
		{"method", "USING ivfflat (vector vector_cosine_ops) WITH (lists=1)", false},
		{"tuning", "USING hnsw (vector vector_cosine_ops) WITH (m=8)", false},
		{"partial", "USING hnsw (vector vector_cosine_ops) WHERE id <> ''", false},
		{"column", "USING btree (id)", false},
		{"metadata operator class", "USING gin (metadata jsonb_path_ops)", true},
		{"metadata expression", "USING gin ((metadata -> 'nested'))", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "docs", Dimension: 2})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE INDEX conflict ON %s %s", qualifiedTable(store.opts.Schema, "docs"), tt.definition)); err != nil {
				t.Fatal(err)
			}
			opts := vectordata.IndexOptions{Vector: &vectordata.VectorIndexOptions{Name: "conflict"}}
			if tt.metadata {
				opts = vectordata.IndexOptions{Metadata: &vectordata.MetadataIndexOptions{Name: "conflict"}}
			}
			if err := collection.EnsureIndexes(ctx, opts); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("expected ErrSchemaMismatch, got %v", err)
			}
		})
	}
	t.Run("different table", func(t *testing.T) {
		store := newTestStore(t, pool)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		first, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "first", Dimension: 2})
		if err != nil {
			t.Fatal(err)
		}
		second, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "second", Dimension: 2})
		if err != nil {
			t.Fatal(err)
		}
		opts := vectordata.IndexOptions{Vector: &vectordata.VectorIndexOptions{Name: "shared"}}
		if err := first.EnsureIndexes(ctx, opts); err != nil {
			t.Fatal(err)
		}
		if err := second.EnsureIndexes(ctx, opts); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("expected ErrSchemaMismatch, got %v", err)
		}
	})
}

func TestIntegrationRejectsIncompatiblePrimaryKeys(t *testing.T) {
	pool := integrationPool(t)
	for _, primaryKey := range []string{
		"PRIMARY KEY (id, tenant)",
		"PRIMARY KEY (tenant, id)",
		"PRIMARY KEY (tenant)",
		"PRIMARY KEY (id) DEFERRABLE",
	} {
		t.Run(primaryKey, func(t *testing.T) {
			store := newTestStore(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := store.ensureBaseSchema(ctx); err != nil {
				t.Fatal(err)
			}
			_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (
				id text, tenant text, vector vector(2) NOT NULL,
				metadata jsonb NOT NULL DEFAULT '{}'::jsonb, content text, %s
			)`, qualifiedTable(store.opts.Schema, "docs"), primaryKey))
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "docs", Dimension: 2})
			if !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("expected ErrSchemaMismatch, got %v", err)
			}
		})
	}
}

func TestIntegrationAutoMigrateValidatesBeforeAddingColumns(t *testing.T) {
	pool := integrationPool(t)
	for _, columns := range []string{
		"id text PRIMARY KEY, vector vector(3) NOT NULL",
		"id text PRIMARY KEY, vector vector(2) NOT NULL, content integer",
		"id text PRIMARY KEY, vector vector NOT NULL",
		"id text PRIMARY KEY, vector vector(2)",
		"id text PRIMARY KEY, vector vector(2) NOT NULL, content text NOT NULL",
	} {
		t.Run(columns, func(t *testing.T) {
			store := newTestStore(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := store.ensureBaseSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (%s)", qualifiedTable(store.opts.Schema, "docs"), columns)); err != nil {
				t.Fatal(err)
			}
			_, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{
				Name: "docs", Dimension: 2, Mode: vectordata.EnsureAutoMigrate,
			})
			if !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Errorf("expected ErrSchemaMismatch, got %v", err)
			}
			var metadataExists bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = $1 AND table_name = 'docs' AND column_name = 'metadata'
			)`, store.opts.Schema).Scan(&metadataExists); err != nil {
				t.Fatal(err)
			}
			if metadataExists {
				t.Fatal("incompatible schema was modified before validation failed")
			}
		})
	}
}

func TestIntegrationRejectsIncompatibleNullability(t *testing.T) {
	pool := integrationPool(t)
	for _, columns := range []string{
		"id text PRIMARY KEY, vector vector(2), metadata jsonb NOT NULL, content text",
		"id text PRIMARY KEY, vector vector(2) NOT NULL, metadata jsonb, content text",
		"id text PRIMARY KEY, vector vector(2) NOT NULL, metadata jsonb NOT NULL, content text NOT NULL",
	} {
		for _, mode := range []vectordata.EnsureMode{vectordata.EnsureStrict, vectordata.EnsureAutoMigrate} {
			t.Run(string(mode)+"/"+columns, func(t *testing.T) {
				store := newTestStore(t, pool)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := store.ensureBaseSchema(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (%s)", qualifiedTable(store.opts.Schema, "docs"), columns)); err != nil {
					t.Fatal(err)
				}
				_, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "docs", Dimension: 2, Mode: mode})
				if !errors.Is(err, vectordata.ErrSchemaMismatch) {
					t.Fatalf("expected ErrSchemaMismatch, got %v", err)
				}
			})
		}
	}
}

func TestIntegrationNullEquality(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "docs", Dimension: 2})
	if err != nil {
		t.Fatal(err)
	}
	content := "hello"
	if err := collection.Insert(ctx, []vectordata.Record{
		{ID: "null", Vector: []float32{1, 0}, Metadata: map[string]any{"value": nil}},
		{ID: "text", Vector: []float32{1, 0}, Content: &content, Metadata: map[string]any{"value": "hello"}},
		{ID: "missing", Vector: []float32{1, 0}, Content: &content},
	}); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []vectordata.Filter{
		vectordata.Eq(vectordata.Column("content"), nil),
		vectordata.Eq(vectordata.Column("content"), (*string)(nil)),
		vectordata.Eq(vectordata.Metadata("value"), nil),
	} {
		count, err := collection.Count(ctx, filter)
		if err != nil || count != 1 {
			t.Errorf("Count(%#v) = %d, %v; want 1, nil", filter, count, err)
		}
		results, err := collection.SearchByVector(ctx, []float32{1, 0}, 3, vectordata.SearchOptions{Filter: filter})
		if err != nil || len(results) != 1 || results[0].Record.ID != "null" {
			t.Errorf("SearchByVector(%#v) = %#v, %v; want record null", filter, results, err)
		}
	}
}
