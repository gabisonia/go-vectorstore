package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Fix selectivity at 10%, a five-record page and a two-record offset. Keep
// vectors large enough to expose whole-collection decoding in the reference.
func BenchmarkSQLiteScalarRead(b *testing.B) {
	for _, size := range []int{100, 10000} {
		b.Run(fmt.Sprintf("records%d", size), func(b *testing.B) {
			ctx := context.Background()
			s, err := Open(ctx, filepath.Join(b.TempDir(), "scalar.db"), StoreOptions{})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { s.Close() })
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Group", Type: vectordata.TypeString}, {Name: "Rank", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 128}}}
			base, err := s.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				b.Fatal(err)
			}
			c := base.(*Collection)
			rows := make([]vectordata.ModelRecord, size)
			for i := range rows {
				group := "other"
				if i%10 == 0 {
					group = "match"
				}
				vector := make([]float32, 128)
				for j := range vector {
					vector[j] = float32((i+j)%7) / 7
				}
				rows[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Group": group, "Rank": int64(size - i)}, Vectors: map[string][]float32{"Vector": vector}}
			}
			if _, err := c.Insert(ctx, rows); err != nil {
				b.Fatal(err)
			}
			filter := vectordata.And(vectordata.Eq(vectordata.Column("Group"), "match"), vectordata.Gte(vectordata.Column("Rank"), int64(0)))
			opts := vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "Rank"}}, Skip: 2}
			for _, ref := range []bool{false, true} {
				prefix := "native"
				if ref {
					prefix = "reference"
				}
				b.Run(prefix+"_query", func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						var query vectordata.ModelCollection = c
						if ref {
							engine, err := c.engine(ctx)
							if err != nil {
								b.Fatal(err)
							}
							query = engine
						}
						result, err := query.Query(ctx, filter, 5, opts)
						if err != nil || len(result) != 5 || result[0].Key != int64(size-30) {
							b.Fatalf("scalar page: %+v %v", result, err)
						}
					}
				})
				b.Run(prefix+"_count", func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						var query vectordata.ModelCollection = c
						if ref {
							engine, err := c.engine(ctx)
							if err != nil {
								b.Fatal(err)
							}
							query = engine
						}
						count, err := query.Count(ctx, filter)
						if err != nil || count != int64(size/10) {
							b.Fatalf("scalar count: %d %v", count, err)
						}
					}
				})
			}
		})
	}
}
