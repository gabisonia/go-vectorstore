//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/samples/internal/demo"
	"github.com/gabisonia/go-vectorstore/stores/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestIntegrationPostgresRecipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dsn := os.Getenv("PGVECTOR_TEST_DSN")
	if dsn == "" {
		image := os.Getenv("PGVECTOR_TEST_IMAGE")
		if image == "" {
			image = "pgvector/pgvector:pg16"
		}
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: image, ExposedPorts: []string{"5432/tcp"}, Env: map[string]string{"POSTGRES_USER": "postgres", "POSTGRES_PASSWORD": "postgres", "POSTGRES_DB": "samples"}, WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute)}, Started: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if err := container.Terminate(cleanup); err != nil {
				t.Error(err)
			}
		})
		host, err := container.Host(ctx)
		if err != nil {
			t.Fatal(err)
		}
		port, err := container.MappedPort(ctx, "5432/tcp")
		if err != nil {
			t.Fatal(err)
		}
		dsn = fmt.Sprintf("postgres://postgres:postgres@%s:%s/samples?sslmode=disable", host, port.Port())
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	opts := postgres.DefaultStoreOptions()
	opts.Schema = fmt.Sprintf("sample_it_%d", time.Now().UnixNano())
	opts.Embedder = demo.DeterministicEmbedder()
	store, err := postgres.NewVectorStore(pool, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanup, `DROP SCHEMA IF EXISTS "`+opts.Schema+`" CASCADE`); err != nil {
			t.Error(err)
		}
	})
	for _, recipe := range []string{"named-vectors", "squared-l2", "iterators", "embedded-iterators", "capabilities", "hybrid", "migration"} {
		t.Run(recipe, func(t *testing.T) {
			var out bytes.Buffer
			// Running twice also checks that fixtures and previews can be reused.
			for i := 0; i < 2; i++ {
				if err := execute(ctx, store, recipe, "demo", &out); err != nil {
					t.Fatal(err)
				}
			}
			if recipe == "hybrid" && (!strings.Contains(out.String(), "costs:") || !strings.Contains(out.String(), "reciprocal_rank_fusion")) {
				t.Fatalf("missing fused results: %s", &out)
			}
			if recipe == "migration" {
				if !strings.Contains(out.String(), "ADD COLUMN") || !strings.Contains(out.String(), "Preview only;") {
					t.Fatalf("missing plan: %s", &out)
				}
				var count int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 AND column_name='Category'", opts.Schema, "demo_migration").Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatal("preview applied a migration")
				}
			}
		})
	}
}
