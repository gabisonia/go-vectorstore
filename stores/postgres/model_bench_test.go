//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

// modelFixture is shared by query-plan tests and benchmarks so both exercise the
// SQL emitted by the public model API, with statistics collected after ingestion.
func modelFixture(tb testing.TB, dimensions, rows int, indexed bool) (*ModelCollection, []vectordata.ModelRecord) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	tb.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, integrationDSN)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(pool.Close)
	schema := fmt.Sprintf("modelbench_%d_%d", time.Now().UnixNano(), schemaSeq.Add(1))
	tb.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE"); err != nil {
			tb.Errorf("drop fixture: %v", err)
		}
	})
	store, err := NewVectorStore(pool, StoreOptions{Schema: schema, EnsureExtension: true, StrictByDefault: true})
	if err != nil {
		tb.Fatal(err)
	}
	d := vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64},
		Data:    []vectordata.DataProperty{{Name: "Category", Type: vectordata.TypeString, IsIndexed: indexed}, {Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: indexed}, {Name: "Tags", Type: vectordata.TypeStringArray, IsIndexed: indexed}},
		Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: dimensions}},
	}
	if indexed {
		d.Vectors[0].Index = &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}
	}
	base, err := store.EnsureModelCollection(ctx, "models", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		tb.Fatal(err)
	}
	c := base.(*ModelCollection)
	records := make([]vectordata.ModelRecord, rows)
	for i := range records {
		category, body, tag := fmt.Sprintf("category%d", i%4), "city business hotel", "city"
		if i%1000 == 0 {
			category, body, tag = "rare", "orchid quiet mountain hotel", "rare"
		}
		vector := make([]float32, dimensions)
		for j := range vector {
			vector[j] = float32((i+1)*(j+3)%97+1) / 97
		}
		records[i] = vectordata.ModelRecord{Key: int64(i + 1), Data: map[string]any{"Category": category, "Body": body, "Tags": []string{tag}}, Vectors: map[string][]float32{"Vector": vector}}
	}
	if _, err := c.Insert(ctx, records); err != nil {
		tb.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ANALYZE "+c.table()); err != nil {
		tb.Fatal(err)
	}
	return c, records
}

func BenchmarkModelWrite(b *testing.B) {
	for _, dimensions := range []int{128, 1536} {
		for _, rows := range []int{100, 1000} {
			for _, upsert := range []bool{false, true} {
				mode := "insert"
				if upsert {
					mode = "upsert_existing"
				}
				b.Run(fmt.Sprintf("dim%d/rows%d/%s", dimensions, rows, mode), func(b *testing.B) {
					c, records := modelFixture(b, dimensions, rows, false)
					ctx := context.Background()
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						b.StopTimer()
						if _, err := c.store.pool.Exec(ctx, "TRUNCATE "+c.table()); err != nil {
							b.Fatal(err)
						}
						if upsert {
							if _, err := c.Insert(ctx, records); err != nil {
								b.Fatal(err)
							}
						}
						b.StartTimer()
						var keys []any
						var err error
						if upsert {
							keys, err = c.Upsert(ctx, records)
						} else {
							keys, err = c.Insert(ctx, records)
						}
						if err != nil || len(keys) != rows {
							b.Fatalf("write keys=%d: %v", len(keys), err)
						}
					}
					b.StopTimer()
					b.ReportMetric(float64(b.N*rows)/b.Elapsed().Seconds(), "rows/s")
					if count, err := c.Count(ctx, nil); err != nil || count != int64(rows) {
						b.Fatalf("count=%d, want=%d: %v", count, rows, err)
					}
				})
			}
		}
	}
}

func BenchmarkModelSearch(b *testing.B) {
	c, records := modelFixture(b, 128, 5000, true)
	ctx := context.Background()
	vector := records[0].Vectors["Vector"]
	for _, projection := range []bool{false, true} {
		for _, mode := range []string{"vector", "filtered", "hybrid"} {
			b.Run(fmt.Sprintf("%s/includeVectors%t", mode, projection), func(b *testing.B) {
				opts := vectordata.ModelSearchOptions{IncludeVectors: projection}
				if mode == "filtered" {
					opts.Filter = vectordata.Eq(vectordata.Column("Category"), "category1")
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					var results []vectordata.ModelSearchResult
					var err error
					if mode == "hybrid" {
						results, err = c.HybridSearch(ctx, vector, "orchid", 20, vectordata.HybridSearchOptions{ModelSearchOptions: opts})
					} else {
						results, err = c.Search(ctx, vector, 20, opts)
					}
					if err != nil || len(results) == 0 {
						b.Fatalf("search results=%d: %v", len(results), err)
					}
				}
			})
		}
	}
}
