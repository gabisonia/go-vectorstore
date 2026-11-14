//go:build integration

package postgres

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIntegrationFloat16RoundingMatchesPostgres(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	if err := store.ensureBaseSchema(ctx); err != nil {
		t.Fatal(err)
	}
	values := []float32{0, -0.1, 0.1, 0.3333, 65504, 65519, -65519, math.SmallestNonzeroFloat32}
	rng := rand.New(rand.NewPCG(7, 19))
	for range 8192 {
		x := float32(math.Ldexp(1+rng.Float64(), rng.IntN(40)-25))
		if rng.IntN(2) == 0 {
			x = -x
		}
		values = append(values, x)
	}
	for exponent := -24; exponent <= 15; exponent++ {
		for mantissa := 0; mantissa < 32; mantissa++ {
			x := float32(math.Ldexp(1+(float64(mantissa)+0.5)/1024, exponent))
			values = append(values, x, -x, math.Nextafter32(x, 0), math.Nextafter32(x, float32(math.Inf(1))))
		}
	}
	for start := 0; start < len(values); start += 1024 {
		input := values[start:min(start+1024, len(values))]
		want, err := vectordata.NormalizeModelVector(input, vectordata.VectorProperty{Name: "Half", Dimensions: len(input), Precision: vectordata.VectorFloat16, Metric: vectordata.DistanceL2})
		if err != nil {
			t.Fatal(err)
		}
		var text string
		if err := store.pool.QueryRow(ctx, "SELECT ($1::vector::halfvec)::vector::text", vectorLiteral(input)).Scan(&text); err != nil {
			t.Fatal(err)
		}
		got, err := parseVectorText(text)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("rounding differs from pgvector at batch %d: %v", start, err)
		}
	}
}

type mixedPrecisionModel struct {
	ID   int64     `vectordata:"key,generated"`
	Half []float32 `vectordata:"vector,dimensions=3,precision=float16,index"`
	Full []float32 `vectordata:"vector,dimensions=3"`
}

func TestIntegrationHalfVectorMixedTransportAndTypedReads(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, integrationPool(t))
	d, err := vectordata.DefinitionFor[mixedPrecisionModel]()
	if err != nil {
		t.Fatal(err)
	}
	handle, err := store.EnsureModelCollection(ctx, "mixed", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	typed, err := vectordata.GetTypedModelCollection[int64, mixedPrecisionModel](store, c.name, nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := []float32{0.1, 0.3333, -0.1234}
	want := []float32{0.0999755859375, 0.333251953125, -0.1234130859375}
	for _, binary := range []bool{false, true} {
		record := vectordata.ModelRecord{Vectors: map[string][]float32{"Half": input, "Full": input}}
		keys, err := c.writeEncoded(ctx, []vectordata.ModelRecord{record}, false, binary)
		if err != nil {
			t.Fatal(err)
		}
		row, err := typed.Get(ctx, keys[0].(int64), vectordata.RetrievalOptions{IncludeVectors: true})
		if err != nil || !reflect.DeepEqual(row.Half, want) || !reflect.DeepEqual(row.Full, input) {
			t.Fatalf("binary=%v: row=%v, error=%v", binary, row, err)
		}
	}
	if _, err := typed.Upsert(ctx, []mixedPrecisionModel{{Half: input, Full: input}}); err != nil {
		t.Fatal(err)
	}
	count := 0
	for result, err := range typed.SearchIter(ctx, input, 3, vectordata.ModelSearchOptions{VectorProperty: "Half", IncludeVectors: true}) {
		if err != nil || !reflect.DeepEqual(result.Item.Half, want) {
			t.Fatalf("typed half search: %v, %v", result, err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("typed search returned %d records", count)
	}
}

func TestIntegrationVectorPrecisionMigrationAndDrift(t *testing.T) {
	store, c, d := migrationFixture(t)
	ctx := context.Background()
	var original string
	registry := qualifiedTable(store.opts.Schema, modelRegistry)
	if err := store.pool.QueryRow(ctx, "SELECT definition::text FROM "+registry+" WHERE name=$1", c.name).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(original, "Precision") {
		t.Fatal("default model gained a precision field")
	}
	d.Vectors[0].Precision = vectordata.VectorFloat32
	if _, err := store.EnsureModelCollection(ctx, c.name, d, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatalf("old registration compatibility: %v", err)
	}
	noop, err := store.PlanMigration(ctx, c.name, d)
	if err != nil || len(noop.Steps) != 0 || noop.Finalization != nil {
		t.Fatalf("explicit float32 changed old registration: %v, %v", noop, err)
	}
	d.Vectors[0].Precision = vectordata.VectorFloat16
	plan, err := store.PlanMigration(ctx, c.name, d)
	if err != nil || !plan.HasManualWork() {
		t.Fatalf("precision conversion should require manual work: %v, %v", plan, err)
	}
	found := false
	for _, step := range plan.Steps {
		if step.Kind == vectordata.MigrationAlterProperty && step.DataLoss && step.Manual && strings.Contains(step.Description, "halfvec(3)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing precision conversion classification: %+v", plan.Steps)
	}
	if err := store.ApplyMigration(ctx, plan); !errors.Is(err, vectordata.ErrMigrationNotAdditive) {
		t.Fatalf("applied precision conversion: %v", err)
	}
	assertOriginalMigrationStorage(t, c)
	if _, err := store.pool.Exec(ctx, "DROP INDEX "+qualifiedTable(store.opts.Schema, "idx_"+c.name+"_embedding_hnsw")+"; ALTER TABLE "+c.table()+" ALTER COLUMN embedding TYPE halfvec(3) USING embedding::halfvec(3)"); err != nil {
		t.Fatal(err)
	}
	drift, err := store.PlanMigration(ctx, c.name, c.definition)
	if err != nil || len(drift.Issues) == 0 || drift.Finalization != nil {
		t.Fatalf("precision drift was not blocked: %v, %v", drift, err)
	}
	if err := c.EnsureExists(ctx); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("ensure accepted changed precision: %v", err)
	}
}

func TestIntegrationHalfVectorIndexBeyond2000Dimensions(t *testing.T) {
	store := newTestStore(t, integrationPool(t))
	ctx := context.Background()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Half", Dimensions: 3072, Precision: vectordata.VectorFloat16, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}}}}
	handle, err := store.EnsureModelCollection(ctx, "large_half", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, 3072)
	vector[0] = 0.1
	if _, err := handle.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Vectors: map[string][]float32{"Half": vector}}}); err != nil {
		t.Fatal(err)
	}
	rows, err := handle.Search(ctx, vector, 1, vectordata.ModelSearchOptions{IncludeVectors: true})
	if err != nil || len(rows) != 1 || len(rows[0].Record.Vectors["Half"]) != 3072 {
		t.Fatalf("3072-dimension half search: %d, %v", len(rows), err)
	}
}

func TestIntegrationHalfVectorStorageAndRecall(t *testing.T) {
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
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Vectors: []vectordata.VectorProperty{{Name: "Full", Dimensions: 32}, {Name: "Half", Dimensions: 32, Precision: vectordata.VectorFloat16}}}
	handle, err := store.EnsureModelCollection(ctx, "half_recall", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	rng := rand.New(rand.NewPCG(41, 73))
	records := make([]vectordata.ModelRecord, 1000)
	for i := range records {
		vector := make([]float32, 32)
		for j := range vector {
			vector[j] = rng.Float32()*2 - 1
		}
		records[i] = vectordata.ModelRecord{Key: int64(i), Vectors: map[string][]float32{"Full": vector, "Half": vector}}
	}
	if _, err := c.Insert(ctx, records); err != nil {
		t.Fatal(err)
	}
	var fullBytes, halfBytes int
	if err := store.pool.QueryRow(ctx, "SELECT pg_column_size(\"Full\"),pg_column_size(\"Half\") FROM "+c.table()+" LIMIT 1").Scan(&fullBytes, &halfBytes); err != nil {
		t.Fatal(err)
	}
	// Small varlena values can use PostgreSQL's shorter header in stored rows.
	if fullBytes != 4*32+8 || halfBytes < 2*32+5 || halfBytes > 2*32+8 {
		t.Fatalf("unexpected vector storage: full=%d, half=%d", fullBytes, halfBytes)
	}
	queries := make([][]float32, 10)
	baseline := make([][]vectordata.ModelSearchResult, len(queries))
	for i := range queries {
		queries[i] = make([]float32, 32)
		for j := range queries[i] {
			queries[i][j] = rng.Float32()*2 - 1
		}
		baseline[i], err = c.Search(ctx, queries[i], 10, vectordata.ModelSearchOptions{VectorProperty: "Full"})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat} {
		t.Run(string(method), func(t *testing.T) {
			desired := c.definition.Clone()
			desired.Vectors[1].Index = &vectordata.VectorIndexOptions{Name: "half_recall_index", Method: method, IVFFlat: vectordata.IVFFlatOptions{Lists: 10}}
			plan, err := store.PlanMigration(ctx, c.name, desired)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ApplyMigration(ctx, plan); err != nil {
				t.Fatal(err)
			}
			updated, err := store.EnsureModelCollection(ctx, c.name, desired, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.pool.Exec(ctx, "ANALYZE "+c.table()+"; SET enable_seqscan=off"); err != nil {
				t.Fatal(err)
			}
			querySQL, queryArgs, _, err := updated.(*ModelCollection).searchPlan(queries[0], 10, vectordata.ModelSearchOptions{VectorProperty: "Half"})
			if err != nil {
				t.Fatal(err)
			}
			explained, err := store.pool.Query(ctx, "EXPLAIN "+querySQL, queryArgs...)
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
			if err != nil || !strings.Contains(explain.String(), "half_recall_index") {
				t.Fatalf("half index was not used: %s, %v", explain.String(), err)
			}
			tuning := SearchTuning{}
			if method == vectordata.IndexMethodHNSW {
				tuning.HNSW = &vectordata.HNSWSearchOptions{EfSearch: 100}
			} else {
				tuning.IVFFlat = &vectordata.IVFFlatSearchOptions{Probes: 9}
			}
			matched := 0
			for i, query := range queries {
				rows, err := updated.Search(ctx, query, 10, vectordata.ModelSearchOptions{VectorProperty: "Half", ProviderOptions: tuning})
				if err != nil || len(rows) != 10 {
					t.Fatalf("indexed half search: %d, %v", len(rows), err)
				}
				keys := map[any]bool{}
				for _, row := range baseline[i] {
					keys[row.Record.Key] = true
				}
				for _, row := range rows {
					if keys[row.Record.Key] {
						matched++
					}
				}
			}
			recall := float64(matched) / float64(10*len(queries))
			if recall < 0.9 {
				t.Fatalf("half precision recall@10=%.2f; want >=0.9", recall)
			}
			t.Logf("%s: recall@10=%.2f, vector column bytes full=%d half=%d", method, recall, fullBytes, halfBytes)
			// Remove this reviewed index/registration pair to test the other method
			// from the same unindexed fixture without permitting index rebuilds in Apply.
			if _, err := store.pool.Exec(ctx, "DROP INDEX "+qualifiedTable(store.opts.Schema, "half_recall_index")); err != nil {
				t.Fatal(err)
			}
			payload, err := definitionJSON(c.definition)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.pool.Exec(ctx, "UPDATE "+qualifiedTable(store.opts.Schema, modelRegistry)+" SET definition=$2::jsonb WHERE name=$1", c.name, payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}
