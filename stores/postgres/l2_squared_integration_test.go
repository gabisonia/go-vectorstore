//go:build integration

package postgres

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationSquaredL2IndexesAndMigration(t *testing.T) {
	for _, precision := range []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16} {
		for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat} {
			t.Run(string(precision)+"_"+string(method), func(t *testing.T) {
				ctx := context.Background()
				cfg, err := pgxpool.ParseConfig(integrationDSN)
				if err != nil {
					t.Fatal(err)
				}
				cfg.MaxConns = 1
				pool, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(pool.Close)
				store := newTestStore(t, pool)
				d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Selected", Dimensions: 2, Metric: vectordata.DistanceL2Squared, Precision: precision}}}
				h, err := store.EnsureModelCollection(ctx, "squared_indexes", d, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				c := h.(*ModelCollection)
				records := make([]vectordata.ModelRecord, 1000)
				for i := range records {
					records[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Body": "guide"}, Vectors: map[string][]float32{"Selected": {float32(i), 1}}}
				}
				if _, err := c.Insert(ctx, records); err != nil {
					t.Fatal(err)
				}
				query := []float32{0, 1}
				exact, err := c.Search(ctx, query, 10, vectordata.ModelSearchOptions{})
				if err != nil || len(exact) != 10 {
					t.Fatalf("exact squared search: %v, %v", exact, err)
				}
				d.Vectors[0].Index = &vectordata.VectorIndexOptions{Method: method}
				if method == vectordata.IndexMethodIVFFlat {
					d.Vectors[0].Index.IVFFlat.Lists = 10
				}
				plan, err := store.PlanMigration(ctx, c.name, d)
				if err != nil || plan.HasManualWork() {
					t.Fatalf("squared index migration: %+v, %v", plan, err)
				}
				if err := store.ApplyMigration(ctx, plan); err != nil {
					t.Fatal(err)
				}
				h, err = store.EnsureModelCollection(ctx, c.name, d, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				c = h.(*ModelCollection)
				if _, err := pool.Exec(ctx, "ANALYZE "+c.table()); err != nil {
					t.Fatal(err)
				}
				opts := vectordata.ModelSearchOptions{}
				if method == vectordata.IndexMethodHNSW {
					opts.ProviderOptions = SearchTuning{HNSW: &vectordata.HNSWSearchOptions{EfSearch: 1000}}
				} else {
					opts.ProviderOptions = SearchTuning{IVFFlat: &vectordata.IVFFlatSearchOptions{Probes: 10, IterativeScan: true, MaxProbes: 10}}
				}
				sql, args, _, err := c.searchPlan(query, 10, opts)
				if err != nil {
					t.Fatal(err)
				}
				hybridSQL, hybridArgs, _, err := c.hybridPlan(query, "guide", 10, vectordata.HybridSearchOptions{ModelSearchOptions: opts})
				if err != nil {
					t.Fatal(err)
				}
				indexName := "idx_squared_indexes_Selected_" + string(method)
				// Force eligibility, not a performance claim about arbitrary data/statistics.
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
					t.Fatal(err)
				}
				for _, p := range []struct {
					sql  string
					args []any
				}{{sql, args}, {hybridSQL, hybridArgs}} {
					var explain []byte
					if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+p.sql, p.args...).Scan(&explain); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(explain), indexName) {
						t.Fatalf("squared ordering lost the L2 index: %s", explain)
					}
				}
				if err := tx.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				got, err := c.Search(ctx, query, 10, opts)
				if err != nil || len(got) != len(exact) {
					t.Fatalf("indexed squared search: %v, %v", got, err)
				}
				for i, hit := range got {
					want := float64(i * i)
					if hit.Record.Key != exact[i].Record.Key || math.Abs(hit.Distance-want) > 1e-6 || math.Abs(hit.Score-1/(1+want)) > 1e-12 {
						t.Fatalf("indexed distance/score at %d: %+v", i, hit)
					}
				}
				// L2 and squared L2 share a physical operator class, but changing the
				// registered metric still needs semantic review for scores/thresholds.
				linear := c.Definition()
				linear.Vectors[0].Metric = vectordata.DistanceL2
				linear.Vectors[0].Index.Metric = vectordata.DistanceL2
				change, err := store.PlanMigration(ctx, c.name, linear)
				if err != nil || !change.HasManualWork() {
					t.Fatalf("metric change bypassed review: %+v, %v", change, err)
				}
				if err := store.ApplyMigration(ctx, change); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
					t.Fatalf("applied a semantic metric change: %v", err)
				}
			})
		}
	}
}

func TestIntegrationRecordSquaredL2(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	c, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "squared_record", Dimension: 2, Metric: vectordata.DistanceL2Squared})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Upsert(ctx, []vectordata.Record{{ID: "a", Vector: []float32{0, 0}}, {ID: "b", Vector: []float32{3, 4}}, {ID: "c", Vector: []float32{6, 8}}}); err != nil {
		t.Fatal(err)
	}
	threshold := 5.0
	got, err := c.SearchByVector(ctx, []float32{0, 0}, 3, vectordata.SearchOptions{Threshold: &threshold})
	if err != nil || len(got) != 1 || got[0].Record.ID != "a" {
		t.Fatalf("Record threshold units: %v, %v", got, err)
	}
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat} {
		if err := c.EnsureIndexes(ctx, vectordata.IndexOptions{Vector: &vectordata.VectorIndexOptions{Name: "record_" + string(method), Method: method, IVFFlat: vectordata.IVFFlatOptions{Lists: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err = c.SearchByVector(ctx, []float32{0, 0}, 3, vectordata.SearchOptions{})
	if err != nil || len(got) != 3 || got[1].Distance != 25 || got[1].Score != 1.0/26 {
		t.Fatalf("Record squared distance/score: %v, %v", got, err)
	}
}
