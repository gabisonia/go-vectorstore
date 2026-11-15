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

func testBinary(t *testing.T, text string) vectordata.BinaryVector {
	t.Helper()
	v, err := vectordata.ParseBinaryVector(text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIntegrationBinaryTransportAndTypeDrift(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 9}, {Name: "Dense", Dimensions: 2}, {Name: "Half", Dimensions: 2, Precision: vectordata.VectorFloat16}}}
	h, err := store.EnsureModelCollection(ctx, "binary_transport", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := h.(*ModelCollection)
	for _, binary := range []bool{false, true} {
		for _, text := range []string{"000000000", "111111111", "101010101"} {
			v := testBinary(t, text)
			record := vectordata.ModelRecord{Key: "a", Vectors: map[string][]float32{"Dense": {1, 0}, "Half": {1, 0}}, BinaryVectors: map[string]vectordata.BinaryVector{"Bits": v}}
			if _, err := c.writeEncoded(ctx, []vectordata.ModelRecord{record}, true, binary); err != nil {
				t.Fatal(err)
			}
			row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
			if err != nil || !reflect.DeepEqual(row.BinaryVectors["Bits"], v) || !reflect.DeepEqual(row.Vectors, record.Vectors) {
				t.Fatalf("binary=%v transport: %v, %v", binary, row, err)
			}
			var bits, bytes int
			if err := store.pool.QueryRow(ctx, "SELECT bit_length(\"Bits\"),octet_length(\"Bits\") FROM "+c.table()+" LIMIT 1").Scan(&bits, &bytes); err != nil || bits != 9 || bytes != 2 {
				t.Fatalf("packed storage: %d bits, %d bytes, %v", bits, bytes, err)
			}
		}
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE "+c.table()+` ALTER COLUMN "Bits" TYPE bit(8) USING "Bits"::bit(8)`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true}); !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("out-of-band bit length accepted: %v", err)
	}
	if err := c.EnsureExists(ctx); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("binary column drift accepted: %v", err)
	}
}

func TestIntegrationBinaryIndexesAndIteratorCleanup(t *testing.T) {
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat} {
		t.Run(string(method), func(t *testing.T) {
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
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Group", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 17}}}
			h, err := store.EnsureModelCollection(ctx, "binary_index", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			c := h.(*ModelCollection)
			records := make([]vectordata.ModelRecord, 1000)
			for i := range records {
				v := vectordata.BinaryVector{Dimensions: 17, Bytes: []byte{byte(i >> 8), byte(i), byte(i%2) << 7}}
				records[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Group": i % 2}, BinaryVectors: map[string]vectordata.BinaryVector{"Bits": v}}
			}
			if _, err := c.Insert(ctx, records); err != nil {
				t.Fatal(err)
			}
			d.Vectors[0].Index = &vectordata.VectorIndexOptions{Method: method, IVFFlat: vectordata.IVFFlatOptions{Lists: 8}}
			plan, err := store.PlanMigration(ctx, c.name, d)
			if err != nil || plan.HasManualWork() {
				t.Fatalf("binary index migration: %+v, %v", plan, err)
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
			query := testBinary(t, "00000000000000000")
			opts := vectordata.ModelSearchOptions{IncludeVectors: true, Filter: vectordata.Eq(vectordata.Column("Group"), 0)}
			if method == vectordata.IndexMethodIVFFlat {
				opts.ProviderOptions = SearchTuning{IVFFlat: &vectordata.IVFFlatSearchOptions{Probes: 2}}
			} else {
				opts.ProviderOptions = SearchTuning{HNSW: &vectordata.HNSWSearchOptions{EfSearch: 100}}
			}
			sql, args, _, err := c.binarySearchPlan(query, 5, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(sql, "::bit(17)") {
				t.Fatalf("unbounded bit cast can truncate query: %s", sql)
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
				if index.property == "Bits" {
					found = index.method == string(method) && index.opClass == "bit_hamming_ops" && strings.Contains(explain.String(), index.name)
				}
			}
			if !found {
				t.Fatalf("binary index not eligible: %s", explain.String())
			}
			hits, err := c.SearchBinary(ctx, query, 5, opts)
			if err != nil || len(hits) == 0 || hits[0].Record.Key != int64(0) || hits[0].Distance != 0 || hits[0].Score != 1 || !reflect.DeepEqual(hits[0].Record.BinaryVectors["Bits"], query) {
				t.Fatalf("indexed binary search: %v, %v", hits, err)
			}
			for _, err := range c.SearchBinaryIter(ctx, query, 5, opts) {
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			if n, err := c.Count(ctx, nil); err != nil || n != 1000 {
				t.Fatalf("binary iterator leaked single connection: %d, %v", n, err)
			}
		})
	}
}

func TestIntegrationBinaryMigrationClassifications(t *testing.T) {
	ctx := context.Background()
	store, c, d := migrationFixture(t)
	d.Vectors = append(d.Vectors, vectordata.VectorProperty{Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 3})
	plan, err := store.PlanMigration(ctx, c.name, d)
	if err != nil || !plan.HasManualWork() {
		t.Fatalf("binary addition needs backfill: %+v, %v", plan, err)
	}
	found := false
	for _, step := range plan.Steps {
		found = found || step.Property == "Bits" && step.RequiresBackfill && step.Manual
	}
	if !found {
		t.Fatalf("missing binary backfill: %+v", plan)
	}
	if err := store.ApplyMigration(ctx, plan); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
		t.Fatalf("binary addition applied without backfill: %v", err)
	}
	d = c.definition.Clone()
	d.Vectors[0].Kind = vectordata.VectorBinary
	d.Vectors[0].Metric = vectordata.DistanceHamming
	d.Vectors[0].SourceProperty = ""
	d.Vectors[0].Embedder = nil
	d.Vectors[0].Index = nil
	plan, err = store.PlanMigration(ctx, c.name, d)
	if err != nil || !plan.HasManualWork() {
		t.Fatalf("dense-to-binary conversion: %+v, %v", plan, err)
	}
	found = false
	for _, step := range plan.Steps {
		found = found || step.Kind == vectordata.MigrationAlterProperty && step.Manual && step.RequiresBackfill && step.RequiresReembedding && step.SQL == ""
	}
	if !found {
		t.Fatalf("unsafe binary conversion: %+v", plan)
	}
	if err := store.ApplyMigration(ctx, plan); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
		t.Fatalf("binary conversion applied: %v", err)
	}
	assertOriginalMigrationStorage(t, c)
}

func TestIntegrationBinaryMissingPrerequisites(t *testing.T) {
	ctx := context.Background()
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
	store := newTestStore(t, pool)
	store.opts.EnsureExtension = false
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}
	if _, err := store.EnsureModelCollection(ctx, "scalar", d, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatal(err)
	}
	d.Vectors = []vectordata.VectorProperty{{Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 3, Index: &vectordata.VectorIndexOptions{}}}
	plan, err := store.PlanMigration(ctx, "scalar", d)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, step := range plan.Steps {
		found = found || step.Kind == vectordata.MigrationPrerequisite && strings.Contains(step.Description, "binary Hamming")
	}
	if !found {
		t.Fatalf("missing binary prerequisites: %+v", plan)
	}
	if _, err := store.EnsureModelCollection(ctx, "missing_binary", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("missing binary support: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualifiedTable(store.opts.Schema, "missing_binary")).Scan(&exists); err != nil || exists {
		t.Fatalf("unusable binary table created: %v, %v", exists, err)
	}
}
