//go:build integration

package redis

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// BenchmarkRedisNativeRead compares native reads with explicit full-snapshot
// engine construction. Setup/inserts/index construction are outside the timers.
func BenchmarkRedisNativeRead(b *testing.B) {
	ctx := context.Background()
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprintf("records%d", size), func(b *testing.B) {
			id, err := vectordata.NewUUIDKey()
			if err != nil {
				b.Fatal(err)
			}
			s, err := New(StoreOptions{Addr: integrationAddr, Namespace: "bench_" + id, NativeIndexes: true})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { s.DeleteCollection(ctx, "records"); s.Close() })
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Group", Type: vectordata.TypeString, IsIndexed: true}, {Name: "Rank", Type: vectordata.TypeInt64, IsIndexed: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 16, Metric: vectordata.DistanceL2, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}}}}
			base, err := s.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				b.Fatal(err)
			}
			c := base.(*Collection)
			rng := rand.New(rand.NewSource(44))
			records := make([]vectordata.ModelRecord, size)
			for i := range records {
				v := make([]float32, 16)
				for j := range v {
					v[j] = rng.Float32()
				}
				group := "other"
				if i%10 == 0 {
					group = "match"
				}
				records[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Group": group, "Rank": int64(size - i)}, Vectors: map[string][]float32{"Vector": v}}
			}
			if _, err := c.Insert(ctx, records); err != nil {
				b.Fatal(err)
			}
			filter := vectordata.And(vectordata.Eq(vectordata.Column("Group"), "match"), vectordata.Gte(vectordata.Column("Rank"), int64(0)))
			qopts := vectordata.QueryOptions{Skip: 2, OrderBy: []vectordata.SortField{{Property: "Rank"}}}
			query := records[0].Vectors["Vector"]
			for _, op := range []string{"query", "count", "search"} {
				for _, native := range []bool{true, false} {
					name := "reference_" + op
					if native {
						name = "native_" + op
					}
					b.Run(name, func(b *testing.B) {
						b.ReportAllocs()
						for range b.N {
							if native {
								switch op {
								case "query":
									rows, err := c.Query(ctx, filter, 5, qopts)
									if err != nil || len(rows) != 5 {
										b.Fatalf("query %d %v", len(rows), err)
									}
								case "count":
									n, err := c.Count(ctx, filter)
									if err != nil || n != int64(size/10) {
										b.Fatalf("count %d %v", n, err)
									}
								case "search":
									hits, err := c.Search(ctx, query, 5, vectordata.ModelSearchOptions{})
									if err != nil || len(hits) != 5 {
										b.Fatalf("search %d %v", len(hits), err)
									}
								}
							} else {
								engine, err := c.engine(ctx)
								if err != nil {
									b.Fatal(err)
								}
								switch op {
								case "query":
									rows, err := engine.Query(ctx, filter, 5, qopts)
									if err != nil || len(rows) != 5 {
										b.Fatalf("query %d %v", len(rows), err)
									}
								case "count":
									n, err := engine.Count(ctx, filter)
									if err != nil || n != int64(size/10) {
										b.Fatalf("count %d %v", n, err)
									}
								case "search":
									hits, err := engine.Search(ctx, query, 5, vectordata.ModelSearchOptions{})
									if err != nil || len(hits) != 5 {
										b.Fatalf("search %d %v", len(hits), err)
									}
								}
							}
						}
					})
				}
			}
		})
	}
}
