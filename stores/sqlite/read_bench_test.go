package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Keep request size fixed while increasing stored records. Setup and inserts are
// outside the timed region; payloads include a 128-component dense vector.
func BenchmarkSQLiteRead(b *testing.B) {
	for _, size := range []int{100, 10000} {
		b.Run(fmt.Sprintf("records%d", size), func(b *testing.B) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(b.TempDir(), "bench.db"), StoreOptions{})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { store.Close() })
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 128, Metric: vectordata.DistanceL2}}}
			c, err := store.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				b.Fatal(err)
			}
			records := make([]vectordata.ModelRecord, size)
			for i := range records {
				vector := make([]float32, 128)
				for j := range vector {
					vector[j] = float32((i+j)%7) / 7
				}
				records[i] = vectordata.ModelRecord{Key: fmt.Sprintf("key-%06d", i), Data: map[string]any{"Title": "sample document"}, Vectors: map[string][]float32{"Vector": vector}}
			}
			if _, err := c.Insert(ctx, records); err != nil {
				b.Fatal(err)
			}
			b.Run("get_many_4", func(b *testing.B) {
				b.ReportAllocs()
				keys := []any{"key-000000", "key-000003", "missing", "key-000000"}
				for b.Loop() {
					rows, err := c.GetMany(ctx, keys, vectordata.RetrievalOptions{})
					if err != nil || len(rows) != 2 {
						b.Fatalf("get many: %d %v", len(rows), err)
					}
				}
			})
			b.Run("get_many_reference", func(b *testing.B) {
				b.ReportAllocs()
				keys := []any{"key-000000", "key-000003", "missing", "key-000000"}
				for b.Loop() {
					rows, err := c.Query(ctx, vectordata.In(vectordata.Column("ID"), keys...), len(keys), vectordata.QueryOptions{})
					if err != nil || len(rows) != 2 {
						b.Fatalf("reference lookup: %d %v", len(rows), err)
					}
				}
			})
			b.Run("count_all", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					n, err := c.Count(ctx, nil)
					if err != nil || n != int64(size) {
						b.Fatalf("count: %d %v", n, err)
					}
				}
			})
			b.Run("count_reference", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					engine, err := c.(*Collection).engine(ctx)
					if err != nil {
						b.Fatal(err)
					}
					n, err := engine.Count(ctx, nil)
					if err != nil || n != int64(size) {
						b.Fatalf("reference count: %d %v", n, err)
					}
				}
			})
		})
	}
}
