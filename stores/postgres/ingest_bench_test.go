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

func BenchmarkIngest(b *testing.B) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, integrationDSN)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()
	for _, dimension := range []int{128, 1536} {
		for _, rows := range []int{500, 5000} {
			for _, upsert := range []bool{false, true} {
				b.Run(fmt.Sprintf("dim%d/rows%d/upsert%t", dimension, rows, upsert), func(b *testing.B) {
					schema := fmt.Sprintf("bench_%d", time.Now().UnixNano())
					store, err := NewVectorStore(pool, StoreOptions{Schema: schema, EnsureExtension: true, StrictByDefault: true})
					if err != nil {
						b.Fatal(err)
					}
					collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "ingest", Dimension: dimension})
					if err != nil {
						b.Fatal(err)
					}
					defer pool.Exec(ctx, "DROP SCHEMA "+quoteIdent(schema)+" CASCADE")
					records := benchmarkRecords(rows, dimension)
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						b.StopTimer()
						if _, err := pool.Exec(ctx, "TRUNCATE "+qualifiedTable(schema, "ingest")); err != nil {
							b.Fatal(err)
						}
						b.StartTimer()
						var err error
						if upsert {
							err = collection.Upsert(ctx, records)
						} else {
							err = collection.Insert(ctx, records)
						}
						if err != nil {
							b.Fatal(err)
						}
					}
					b.StopTimer()
					count, err := collection.Count(ctx, nil)
					if err != nil || count != int64(rows) {
						b.Fatalf("ingest count %d, want %d: %v", count, rows, err)
					}
				})
			}
		}
	}
}
