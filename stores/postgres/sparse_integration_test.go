//go:build integration

package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

func pgSparse(t *testing.T, dim int, indices []int, values []float32) vectordata.SparseVector {
	t.Helper()
	v, err := vectordata.NewSparseVector(dim, indices, values)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIntegrationSparseTransportAndBounds(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 1_000_000_000, Metric: vectordata.DistanceL2}, {Name: "Dense", Dimensions: 2}, {Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 9}}}
	h, err := store.EnsureModelCollection(ctx, "sparse_transport", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := h.(*ModelCollection)
	for _, binary := range []bool{false, true} {
		v := pgSparse(t, 1_000_000_000, []int{0, 999999999}, []float32{1, -2})
		r := vectordata.ModelRecord{Key: "a", Vectors: map[string][]float32{"Dense": {1, 0}}, BinaryVectors: map[string]vectordata.BinaryVector{"Bits": testBinary(t, "101010101")}, SparseVectors: map[string]vectordata.SparseVector{"Sparse": v}}
		if _, err := c.writeEncoded(ctx, []vectordata.ModelRecord{r}, true, binary); err != nil {
			t.Fatal(err)
		}
		row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
		if err != nil || !reflect.DeepEqual(row.SparseVectors, r.SparseVectors) || !reflect.DeepEqual(row.BinaryVectors, r.BinaryVectors) || !reflect.DeepEqual(row.Vectors, r.Vectors) {
			t.Fatalf("binary=%v mixed sparse transport: %v, %v", binary, row, err)
		}
		var size int
		if err := store.pool.QueryRow(ctx, "SELECT octet_length(sparsevec_send(\"Sparse\")) FROM "+c.table()+" LIMIT 1").Scan(&size); err != nil || size != 28 {
			t.Fatalf("sparse serialization expanded dimensions: %d, %v", size, err)
		}
	}
	indices, values := make([]int, 16000), make([]float32, 16000)
	for i := range indices {
		indices[i] = i
		values[i] = 0.125
	}
	wide := pgSparse(t, 1_000_000_000, indices, values)
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "wide", Vectors: map[string][]float32{"Dense": {1, 0}}, BinaryVectors: map[string]vectordata.BinaryVector{"Bits": testBinary(t, "000000000")}, SparseVectors: map[string]vectordata.SparseVector{"Sparse": wide}}}); err != nil {
		t.Fatal(err)
	}
	row, err := c.Get(ctx, "wide", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || !reflect.DeepEqual(row.SparseVectors["Sparse"], wide) {
		t.Fatalf("maximum sparse storage round trip: %v", err)
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE "+c.table()+` ALTER COLUMN "Sparse" TYPE sparsevec(2) USING '{1:1}/2'::sparsevec`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true}); !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("stored sparse dimensions accepted: %v", err)
	}
	if err := c.EnsureExists(ctx); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("sparse schema drift accepted: %v", err)
	}
}

func TestIntegrationSparseHNSWPlansAndCleanup(t *testing.T) {
	for _, metric := range []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct, vectordata.DistanceL1} {
		t.Run(string(metric), func(t *testing.T) {
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
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Group", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 1_000_000_000, Metric: metric}}}
			h, err := store.EnsureModelCollection(ctx, "sparse_index", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			c := h.(*ModelCollection)
			query := pgSparse(t, 1_000_000_000, []int{0}, []float32{1})
			records := make([]vectordata.ModelRecord, 1000)
			for i := range records {
				v := query
				if i > 0 {
					v = pgSparse(t, query.Dimensions, []int{0, 999999000 + i}, []float32{1 - float32(i)/2000, 1})
				}
				records[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Group": i % 2}, SparseVectors: map[string]vectordata.SparseVector{"Sparse": v}}
			}
			if _, err := c.Insert(ctx, records); err != nil {
				t.Fatal(err)
			}
			d.Vectors[0].Index = &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}
			plan, err := store.PlanMigration(ctx, c.name, d)
			if err != nil || plan.HasManualWork() {
				t.Fatalf("sparse index migration: %+v, %v", plan, err)
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
			opts := vectordata.ModelSearchOptions{IncludeVectors: true, Filter: vectordata.Eq(vectordata.Column("Group"), 0), ProviderOptions: SearchTuning{HNSW: &vectordata.HNSWSearchOptions{EfSearch: 100}}}
			sql, args, _, err := c.sparseSearchPlan(query, 5, opts)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTx(ctx, tx)
			if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			var explain strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				explain.WriteString(line)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			indexes, err := modelIndexSpecs(c.name, c.definition)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, index := range indexes {
				if index.property == "Sparse" {
					found = index.method == "hnsw" && strings.HasPrefix(index.opClass, "sparsevec_") && strings.Contains(explain.String(), index.name)
				}
			}
			if !found {
				t.Fatalf("sparse HNSW not eligible: %s", explain.String())
			}
			hits, err := c.SearchSparse(ctx, query, 5, opts)
			distance := 0.0
			if metric == vectordata.DistanceInnerProduct {
				distance = -1
			}
			if err != nil || len(hits) == 0 || hits[0].Record.Key != int64(0) || hits[0].Distance != distance || !reflect.DeepEqual(hits[0].Record.SparseVectors["Sparse"], query) {
				t.Fatalf("indexed sparse search: %v, %v", hits, err)
			}
			for _, err := range c.SearchSparseIter(ctx, query, 5, opts) {
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			if n, err := c.Count(ctx, nil); err != nil || n != 1000 {
				t.Fatalf("sparse iterator leaked single connection: %d, %v", n, err)
			}
			invalid := opts
			invalid.ProviderOptions = SearchTuning{IVFFlat: &vectordata.IVFFlatSearchOptions{Probes: 1}}
			if _, err := c.SearchSparse(ctx, query, 1, invalid); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
				t.Fatalf("sparse IVFFlat tuning accepted: %v", err)
			}
		})
	}
}

func TestIntegrationSparseIndexChecksExistingNonzeros(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 2000, Metric: vectordata.DistanceL2}}}
	h, err := store.EnsureModelCollection(ctx, "sparse_bounds", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.Vectors[0].Index = &vectordata.VectorIndexOptions{}
	reviewed, err := store.PlanMigration(ctx, h.Name(), d)
	if err != nil || reviewed.HasManualWork() {
		t.Fatalf("empty sparse index plan: %+v, %v", reviewed, err)
	}
	indices, values := make([]int, 1001), make([]float32, 1001)
	for i := range indices {
		indices[i] = i
		values[i] = 1
	}
	if _, err := h.Insert(ctx, []vectordata.ModelRecord{{Key: "oversized", SparseVectors: map[string]vectordata.SparseVector{"Sparse": pgSparse(t, 2000, indices, values)}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyMigration(ctx, reviewed); !errors.Is(err, vectordata.ErrMigrationConflict) {
		t.Fatalf("changed sparse data did not invalidate review: %v", err)
	}
	plan, err := store.PlanMigration(ctx, h.Name(), d)
	if err != nil || !plan.HasManualWork() {
		t.Fatalf("oversized sparse index plan: %+v, %v", plan, err)
	}
	found := false
	for _, step := range plan.Steps {
		found = found || step.Property == "Sparse" && step.Manual && step.RequiresBackfill
	}
	if !found {
		t.Fatalf("missing nonzero backfill: %+v", plan)
	}
	if err := store.ApplyMigration(ctx, plan); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
		t.Fatalf("oversized sparse index applied: %v", err)
	}
	if err := h.EnsureExists(ctx); err != nil {
		t.Fatalf("failed index plan changed storage: %v", err)
	}
	if _, err := h.Delete(ctx, []any{"oversized"}); err != nil {
		t.Fatal(err)
	}
	plan, err = store.PlanMigration(ctx, h.Name(), d)
	if err != nil || plan.HasManualWork() {
		t.Fatalf("cleared sparse index plan: %+v, %v", plan, err)
	}
	if err := store.ApplyMigration(ctx, plan); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationSparseMigrationAndMissingPrerequisites(t *testing.T) {
	ctx := context.Background()
	store, c, d := migrationFixture(t)
	d.Vectors = append(d.Vectors, vectordata.VectorProperty{Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 100})
	plan, err := store.PlanMigration(ctx, c.name, d)
	if err != nil || !plan.HasManualWork() {
		t.Fatalf("sparse addition needs backfill: %+v, %v", plan, err)
	}
	if err := store.ApplyMigration(ctx, plan); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
		t.Fatalf("sparse field applied without backfill: %v", err)
	}
	d = c.definition.Clone()
	d.Vectors[0].Kind = vectordata.VectorSparse
	d.Vectors[0].SourceProperty = ""
	d.Vectors[0].Embedder = nil
	d.Vectors[0].Index = nil
	plan, err = store.PlanMigration(ctx, c.name, d)
	if err != nil || !plan.HasManualWork() {
		t.Fatalf("dense-to-sparse conversion: %+v, %v", plan, err)
	}
	found := false
	for _, step := range plan.Steps {
		found = found || step.Kind == vectordata.MigrationAlterProperty && step.Manual && step.RequiresBackfill && step.RequiresReembedding && step.SQL == ""
	}
	if !found {
		t.Fatalf("unsafe sparse conversion: %+v", plan)
	}
	assertOriginalMigrationStorage(t, c)
	cfg, err := pgxpool.ParseConfig(integrationDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "pg_catalog"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	hidden := newTestStore(t, pool)
	hidden.opts.EnsureExtension = false
	d = vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}
	if _, err := hidden.EnsureModelCollection(ctx, "scalar", d, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	d.Vectors = []vectordata.VectorProperty{{Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 3}}
	plan, err = hidden.PlanMigration(ctx, "scalar", d)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, step := range plan.Steps {
		found = found || step.Kind == vectordata.MigrationPrerequisite && strings.Contains(step.Description, "sparsevec")
	}
	if !found {
		t.Fatalf("missing sparse prerequisite: %+v", plan)
	}
	if _, err := hidden.EnsureModelCollection(ctx, "missing_sparse", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("missing sparse support accepted: %v", err)
	}
}
