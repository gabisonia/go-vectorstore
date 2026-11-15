//go:build integration

package postgres

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationL1HNSWRecallAndMigration(t *testing.T) {
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
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Full", Dimensions: 4, Metric: vectordata.DistanceL1}, {Name: "Half", Dimensions: 4, Precision: vectordata.VectorFloat16, Metric: vectordata.DistanceL1}}}
	handle, err := store.EnsureModelCollection(ctx, "l1_indexes", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	rng := rand.New(rand.NewPCG(51, 97))
	records := make([]vectordata.ModelRecord, 1000)
	for i := range records {
		vector := make([]float32, 4)
		if i != 0 {
			for j := range vector {
				vector[j] = rng.Float32()*2 - 1
			}
		}
		records[i] = vectordata.ModelRecord{Key: strconv.Itoa(i), Vectors: map[string][]float32{"Full": vector, "Half": vector}}
	}
	if _, err := c.Insert(ctx, records); err != nil {
		t.Fatal(err)
	}
	query := []float32{0.31, -0.27, 0.1, 0.44}
	baseline := map[string][]vectordata.ModelSearchResult{}
	for _, p := range d.Vectors {
		baseline[p.Name], err = c.Search(ctx, query, 10, vectordata.ModelSearchOptions{VectorProperty: p.Name})
		if err != nil || len(baseline[p.Name]) != 10 {
			t.Fatalf("exact L1 search: %v", err)
		}
	}
	d = c.definition.Clone()
	d.Vectors[0].Index = &vectordata.VectorIndexOptions{Name: "full_l1_index", Method: vectordata.IndexMethodHNSW}
	d.Vectors[1].Index = &vectordata.VectorIndexOptions{Name: "half_l1_index", Method: vectordata.IndexMethodHNSW}
	plan, err := store.PlanMigration(ctx, c.name, d)
	if err != nil || plan.HasManualWork() {
		t.Fatalf("L1 index addition plan: %+v, %v", plan, err)
	}
	if err := store.ApplyMigration(ctx, plan); err != nil {
		t.Fatal(err)
	}
	updated, err := store.EnsureModelCollection(ctx, c.name, d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ANALYZE "+c.table()+"; SET enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Vectors {
		t.Run(p.Name, func(t *testing.T) {
			opts := vectordata.ModelSearchOptions{VectorProperty: p.Name, ProviderOptions: SearchTuning{HNSW: &vectordata.HNSWSearchOptions{EfSearch: 100}}}
			sql, args, _, err := updated.(*ModelCollection).searchPlan(query, 10, opts)
			if err != nil || !strings.Contains(sql, "<+>") {
				t.Fatalf("L1 query: %s, %v", sql, err)
			}
			explained, err := pool.Query(ctx, "EXPLAIN "+sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			var explain strings.Builder
			for explained.Next() {
				var line string
				if err := explained.Scan(&line); err != nil {
					explained.Close()
					t.Fatal(err)
				}
				explain.WriteString(line)
			}
			err = explained.Err()
			explained.Close()
			if err != nil || !strings.Contains(explain.String(), p.Index.Name) {
				t.Fatalf("L1 index unused: %s, %v", explain.String(), err)
			}
			got, err := updated.Search(ctx, query, 10, opts)
			if err != nil || len(got) != 10 {
				t.Fatalf("indexed L1 results: %v, %v", got, err)
			}
			keys := map[any]bool{}
			for _, row := range baseline[p.Name] {
				keys[row.Record.Key] = true
			}
			matches := 0
			for i, row := range got {
				if keys[row.Record.Key] {
					matches++
				}
				if i > 0 && row.Distance < got[i-1].Distance || math.Abs(row.Score-1/(1+row.Distance)) > 1e-12 {
					t.Fatalf("L1 distance/score ordering: %+v", row)
				}
			}
			if matches < 9 {
				t.Fatalf("L1 recall@10=%.2f; want >=0.9", float64(matches)/10)
			}
			t.Logf("%s L1 HNSW recall@10=%.2f", p.Precision, float64(matches)/10)
			zero, err := updated.Search(ctx, []float32{0, 0, 0, 0}, 1, opts)
			if err != nil || len(zero) != 1 || zero[0].Record.Key != "0" || zero[0].Distance != 0 || zero[0].Score != 1 {
				t.Fatalf("indexed zero L1 vector: %v, %v", zero, err)
			}
		})
	}
	noop, err := store.PlanMigration(ctx, c.name, d)
	if err != nil || noop.HasManualWork() || len(noop.Steps) != 0 || noop.Finalization != nil {
		t.Fatalf("registered L1 plan: %+v, %v", noop, err)
	}
}

func TestIntegrationL1MetricChangeNeedsManualReview(t *testing.T) {
	store, c, d := migrationFixture(t)
	d.Vectors[0].Metric = vectordata.DistanceL1
	d.Vectors[0].Index.Metric = vectordata.DistanceL1
	plan, err := store.PlanMigration(context.Background(), c.name, d)
	if err != nil || !plan.HasManualWork() {
		t.Fatalf("metric conversion plan: %+v, %v", plan, err)
	}
	index, review := false, false
	for _, step := range plan.Steps {
		index = index || step.Kind == vectordata.MigrationCreateIndex && strings.Contains(step.SQL, "vector_l1_ops")
		review = review || step.Kind == vectordata.MigrationAlterProperty && step.Manual
	}
	if !index || !review {
		t.Fatalf("L1 metric change classification: %+v", plan.Steps)
	}
	if err := store.ApplyMigration(context.Background(), plan); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
		t.Fatalf("applied metric change: %v", err)
	}
	assertOriginalMigrationStorage(t, c)
}

func TestIntegrationL1RecordAPI(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	c, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "legacy_l1", Dimension: 3, Metric: vectordata.DistanceL1})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Insert(ctx, []vectordata.Record{{ID: "zero", Vector: []float32{0, 0, 0}}, {ID: "near", Vector: []float32{1, 1, 1}}, {ID: "far", Vector: []float32{-1, 2, -3}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureIndexes(ctx, vectordata.IndexOptions{Vector: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}}); err != nil {
		t.Fatal(err)
	}
	threshold := 3.0
	got, err := c.SearchByVector(ctx, []float32{0, 0, 0}, 3, vectordata.SearchOptions{Threshold: &threshold})
	if err != nil || len(got) != 2 || got[0].Record.ID != "zero" || got[0].Score != 1 || got[1].Record.ID != "near" || got[1].Distance != 3 || got[1].Score != 0.25 {
		t.Fatalf("legacy L1 search: %v, %v", got, err)
	}
}

func TestIntegrationL1MissingPrerequisites(t *testing.T) {
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(integrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	// Hide extension types/operators without changing shared extension objects.
	cfg.ConnConfig.RuntimeParams["search_path"] = "pg_catalog"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := newTestStore(t, pool)
	store.opts.EnsureExtension = false
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}
	if _, err := store.EnsureModelCollection(ctx, "scalar", d, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	d.Vectors = []vectordata.VectorProperty{{Name: "Vector", Dimensions: 3, Metric: vectordata.DistanceL1}}
	plan, err := store.PlanMigration(ctx, "scalar", d)
	if err != nil || !plan.HasManualWork() {
		t.Fatalf("L1 prerequisite report: %+v, %v", plan, err)
	}
	found := false
	for _, step := range plan.Steps {
		found = found || step.Kind == vectordata.MigrationPrerequisite && strings.Contains(step.Description, "L1 search")
	}
	if !found {
		t.Fatalf("missing L1 prerequisite: %+v", plan.Steps)
	}
	if _, err := store.EnsureModelCollection(ctx, "missing_l1", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("missing L1 support: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualifiedTable(store.opts.Schema, "missing_l1")).Scan(&exists); err != nil || exists {
		t.Fatalf("created unusable L1 table: %v, %v", exists, err)
	}
}
