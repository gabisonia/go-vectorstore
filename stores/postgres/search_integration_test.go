//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationTunedSearchRecallAndSettingsIsolation(t *testing.T) {
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat} {
		t.Run(string(method), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cfg, err := pgxpool.ParseConfig(integrationDSN)
			if err != nil {
				t.Fatal(err)
			}
			// A single pooled connection makes leakage checks deterministic.
			cfg.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			store := newTestStore(t, pool)
			collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "recall", Dimension: 2})
			if err != nil {
				t.Fatal(err)
			}
			table := qualifiedTable(store.opts.Schema, "recall")
			_, err = pool.Exec(ctx, "INSERT INTO "+table+" (id, vector, metadata) SELECT n::text, ('[1,' || (n::float8 / 2000)::text || ']')::vector, jsonb_build_object('target', n % 100 = 0) FROM generate_series(1,2000) n")
			if err != nil {
				t.Fatal(err)
			}
			filter := vectordata.Eq(vectordata.Metadata("target"), true)
			exact, err := collection.SearchByVector(ctx, []float32{1, 0}, 5, vectordata.SearchOptions{Filter: filter})
			if err != nil || len(exact) != 5 {
				t.Fatalf("exact search: %v, %v", exact, err)
			}
			err = collection.EnsureIndexes(ctx, vectordata.IndexOptions{Vector: &vectordata.VectorIndexOptions{Name: "recall_index", Method: method, IVFFlat: vectordata.IVFFlatOptions{Lists: 20}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "ANALYZE "+table+"; SET enable_seqscan = off"); err != nil {
				t.Fatal(err)
			}
			opts := vectordata.SearchOptions{Filter: filter}
			settingNames := []string{"hnsw.ef_search", "hnsw.iterative_scan", "hnsw.max_scan_tuples"}
			if method == vectordata.IndexMethodHNSW {
				opts.HNSW = &vectordata.HNSWSearchOptions{EfSearch: 20, IterativeScan: true, MaxScanTuples: 10000}
			} else {
				opts.IVFFlat = &vectordata.IVFFlatSearchOptions{Probes: 2, IterativeScan: true, MaxProbes: 20}
				settingNames = []string{"ivfflat.probes", "ivfflat.iterative_scan", "ivfflat.max_probes"}
			}
			before := make(map[string]string)
			for _, name := range settingNames {
				var value string
				if err := pool.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&value); err != nil {
					t.Fatal(err)
				}
				before[name] = value
			}
			plan, err := collection.(*PostgresCollection).buildSearchPlan([]float32{1, 0}, 5, opts)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := pool.Query(ctx, "EXPLAIN "+plan.query, plan.args...)
			if err != nil {
				t.Fatal(err)
			}
			var explain strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				explain.WriteString(line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if !strings.Contains(explain.String(), "recall_index") {
				t.Fatalf("search did not use vector index: %s", explain.String())
			}
			results, err := collection.SearchByVector(ctx, []float32{1, 0}, 5, opts)
			if err != nil || len(results) != 5 {
				t.Fatalf("tuned search returned %d results: %v", len(results), err)
			}
			matches := 0
			for i, result := range results {
				if i > 0 && result.Distance < results[i-1].Distance {
					t.Fatal("distances are not sorted")
				}
				for _, expected := range exact {
					if expected.Record.ID == result.Record.ID {
						matches++
					}
				}
			}
			if matches < 4 {
				t.Fatalf("recall@5 = %.2f; expected >= 0.8", float64(matches)/5)
			}
			assertUnchanged := func() {
				t.Helper()
				for name, value := range before {
					var after string
					if err := pool.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&after); err != nil {
						t.Fatal(err)
					}
					if after != value {
						t.Fatalf("search setting %s leaked: before %s, after %s", name, value, after)
					}
				}
			}
			assertUnchanged()
			opts.Filter = vectordata.Eq(vectordata.Column("id"), func() {})
			if _, err := collection.SearchByVector(ctx, []float32{1, 0}, 5, opts); err == nil {
				t.Fatal("expected query encoding error")
			}
			assertUnchanged()
			t.Logf("%s recall@5=%.2f, request settings restored after success and error", method, float64(matches)/5)
		})
	}
}
