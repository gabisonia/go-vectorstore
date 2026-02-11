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
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

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
