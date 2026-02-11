//go:build integration

package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	_ "github.com/microsoft/go-mssqldb"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	integrationMSSQLUser     = "sa"
	integrationMSSQLPassword = "YourStrong!Passw0rd"
	integrationMSSQLDatabase = "vectorstore_test"
)

var (
	schemaSeq            atomic.Uint64
	integrationDSN       string
	integrationContainer testcontainers.Container
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dsn := strings.TrimSpace(os.Getenv("MSSQL_TEST_DSN"))
	if dsn == "" {
		container, generatedDSN, err := startMSSQLContainer(ctx)
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

func startMSSQLContainer(ctx context.Context) (testcontainers.Container, string, error) {
	request := testcontainers.ContainerRequest{
		Image:        "mcr.microsoft.com/mssql/server:2022-latest",
		ExposedPorts: []string{"1433/tcp"},
		Env: map[string]string{
			"ACCEPT_EULA":       "Y",
			"MSSQL_SA_PASSWORD": integrationMSSQLPassword,
			"MSSQL_PID":         "Developer",
		},
		WaitingFor: wait.ForLog("SQL Server is now ready for client connections").
			WithStartupTimeout(4 * time.Minute),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: request,
		Started:          true,
	})
	if err != nil {
		return nil, "", fmt.Errorf("start sql server container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(context.Background())
		return nil, "", fmt.Errorf("resolve container host: %w", err)
	}

	mappedPort, err := container.MappedPort(ctx, "1433/tcp")
	if err != nil {
		_ = container.Terminate(context.Background())
		return nil, "", fmt.Errorf("resolve container port: %w", err)
	}

	masterDSN := buildMSSQLDSN(host, mappedPort.Port(), "master")
	if err := waitForDatabase(ctx, masterDSN); err != nil {
		_ = container.Terminate(context.Background())
		return nil, "", err
	}
	if err := ensureDatabase(ctx, masterDSN, integrationMSSQLDatabase); err != nil {
		_ = container.Terminate(context.Background())
		return nil, "", err
	}

	dsn := buildMSSQLDSN(host, mappedPort.Port(), integrationMSSQLDatabase)
	if err := waitForDatabase(ctx, dsn); err != nil {
		_ = container.Terminate(context.Background())
		return nil, "", err
	}

	return container, dsn, nil
}

func buildMSSQLDSN(host string, port string, database string) string {
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(integrationMSSQLUser, integrationMSSQLPassword),
		Host:   net.JoinHostPort(host, port),
	}

	query := u.Query()
	query.Set("database", database)
	query.Set("encrypt", "disable")
	u.RawQuery = query.Encode()

	return u.String()
}

func waitForDatabase(parent context.Context, dsn string) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()

	for {
		db, err := sql.Open("sqlserver", dsn)
		if err == nil {
			pingCtx, pingCancel := context.WithTimeout(ctx, 4*time.Second)
			pingErr := db.PingContext(pingCtx)
			pingCancel()
			_ = db.Close()
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
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func ensureDatabase(ctx context.Context, dsn string, database string) error {
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return fmt.Errorf("connect master database: %w", err)
	}
	defer db.Close()

	query := fmt.Sprintf("IF DB_ID(N'%s') IS NULL CREATE DATABASE %s", escapeSQLString(database), quoteIdent(database))
	if _, err := db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("ensure integration database: %w", err)
	}

	return nil
}

func integrationDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := strings.TrimSpace(integrationDSN)
	if dsn == "" {
		t.Fatal("integration DSN is not initialized")
	}

	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("ping db: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	return db
}

func newTestStore(t *testing.T, db *sql.DB) *MSSQLVectorStore {
	t.Helper()

	seq := schemaSeq.Add(1)
	schema := fmt.Sprintf("it_%d_%d", time.Now().UnixNano(), seq)
	schema = strings.ReplaceAll(schema, "-", "_")

	store, err := NewVectorStore(db, StoreOptions{
		Schema:          schema,
		StrictByDefault: true,
	})
	if err != nil {
		t.Fatalf("NewVectorStore: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		cleanupQuery := fmt.Sprintf(`
			DECLARE @schema SYSNAME = N'%s';
			DECLARE @dropSql NVARCHAR(MAX) = N'';
			SELECT @dropSql = @dropSql + N'DROP TABLE ' + QUOTENAME(SCHEMA_NAME(schema_id)) + N'.' + QUOTENAME(name) + N';'
			FROM sys.tables
			WHERE schema_id = SCHEMA_ID(@schema);

			IF LEN(@dropSql) > 0
			BEGIN
				EXEC sp_executesql @dropSql;
			END

			IF SCHEMA_ID(@schema) IS NOT NULL
			BEGIN
				EXEC(N'DROP SCHEMA ' + QUOTENAME(@schema));
			END
		`, escapeSQLString(schema))
		_, _ = db.ExecContext(ctx, cleanupQuery)
	})

	return store
}

func TestIntegrationEnsureCollection(t *testing.T) {
	db := integrationDB(t)
	store := newTestStore(t, db)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

	var count int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = @p1 AND TABLE_NAME = @p2
	`, store.opts.Schema, "docs").Scan(&count)
	if err != nil {
		t.Fatalf("query table exists: %v", err)
	}
	if count == 0 {
		t.Fatalf("expected collection table to exist")
	}

	dimension, metric, found, err := store.readCollectionMetadata(ctx, "docs")
	if err != nil {
		t.Fatalf("readCollectionMetadata: %v", err)
	}
	if !found {
		t.Fatalf("expected collection metadata row to exist")
	}
	if dimension != 3 {
		t.Fatalf("expected dimension 3, got %d", dimension)
	}
	if metric != vectordata.DistanceCosine {
		t.Fatalf("expected metric %q, got %q", vectordata.DistanceCosine, metric)
	}
}

func TestIntegrationUpsertAndGet(t *testing.T) {
	db := integrationDB(t)
	store := newTestStore(t, db)

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

	content := "first"
	if err := collection.Upsert(ctx, []vectordata.Record{{
		ID:      "r1",
		Vector:  []float32{1, 0},
		Content: &content,
		Metadata: map[string]any{
			"category": "news",
			"rank":     1,
		},
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	record, err := collection.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if record.ID != "r1" {
		t.Fatalf("expected ID r1, got %q", record.ID)
	}
	if len(record.Vector) != 2 || record.Vector[0] != 1 || record.Vector[1] != 0 {
		t.Fatalf("unexpected vector: %#v", record.Vector)
	}
	if record.Content == nil || *record.Content != "first" {
		t.Fatalf("unexpected content: %#v", record.Content)
	}
	if record.Metadata["category"] != "news" {
		t.Fatalf("unexpected metadata category: %#v", record.Metadata["category"])
	}

	updated := "updated"
	if err := collection.Upsert(ctx, []vectordata.Record{{
		ID:      "r1",
		Vector:  []float32{0.5, 0.5},
		Content: &updated,
		Metadata: map[string]any{
			"category": "blog",
			"rank":     3,
		},
	}}); err != nil {
		t.Fatalf("Upsert second call: %v", err)
	}

	updatedRecord, err := collection.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get after upsert: %v", err)
	}
	if updatedRecord.Content == nil || *updatedRecord.Content != "updated" {
		t.Fatalf("content not updated: %#v", updatedRecord.Content)
	}
	if updatedRecord.Metadata["category"] != "blog" {
		t.Fatalf("metadata not updated: %#v", updatedRecord.Metadata)
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
			db := integrationDB(t)
			store := newTestStore(t, db)

			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
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
	db := integrationDB(t)
	store := newTestStore(t, db)

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

	results, searchErr := collection.SearchByVector(ctx, []float32{1, 0}, 10, vectordata.SearchOptions{Filter: filter})
	count, countErr := collection.Count(ctx, filter)

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
