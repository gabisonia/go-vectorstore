package sqlitevec_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/stores/sqlitevec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Warm unfiltered L1, 128 dimensions, unique distances, top 10, 4 KiB string
// data. Three handles share one canonical collection; setup/writes are untimed.
func BenchmarkL1Execution(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("records%d", size), func(b *testing.B) {
			ctx := context.Background()
			path := filepath.Join(b.TempDir(), "vectors.db")
			native, err := sqlitevec.Open(ctx, path, sqlitevec.StoreOptions{})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { native.Close() })
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 128, Metric: vectordata.DistanceL1}}}
			c, err := native.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				b.Fatal(err)
			}
			records := make([]vectordata.ModelRecord, size)
			for i := range records {
				v := make([]float32, 128)
				v[0] = float32(i + 1)
				for j := 1; j < len(v); j++ {
					v[j] = float32(j%7) / 7
				}
				records[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Body": strings.Repeat("x", 4096)}, Vectors: map[string][]float32{"V": v}}
			}
			if _, err := c.Insert(ctx, records); err != nil {
				b.Fatal(err)
			}
			collections := map[string]vectordata.ModelCollection{"sqlitevec": c}
			for _, tc := range []struct {
				name    string
				project bool
			}{{"canonical", false}, {"packed_go", true}} {
				s, err := sqlite.Open(ctx, path, sqlite.StoreOptions{VectorProjections: tc.project})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { s.Close() })
				base, err := s.GetModelCollection("records", d, vectordata.ModelCollectionOptions{})
				if err != nil {
					b.Fatal(err)
				}
				collections[tc.name] = base
			}
			query := records[0].Vectors["V"]
			want, err := collections["canonical"].Search(ctx, query, 10, vectordata.ModelSearchOptions{})
			if err != nil {
				b.Fatal(err)
			}
			for _, name := range []string{"canonical", "packed_go", "sqlitevec"} {
				collection := collections[name]
				if got, err := collection.Search(ctx, query, 10, vectordata.ModelSearchOptions{}); err != nil || !reflect.DeepEqual(got, want) {
					b.Fatalf("different %s page: %v", name, err)
				}
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						hits, err := collection.Search(ctx, query, 10, vectordata.ModelSearchOptions{})
						if err != nil || len(hits) != 10 || hits[0].Record.Key != int64(0) || hits[0].Distance != 0 {
							b.Fatalf("invalid page: %+v %v", hits, err)
						}
					}
				})
			}
		})
	}
}
