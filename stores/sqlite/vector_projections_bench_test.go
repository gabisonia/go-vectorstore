package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Warm unfiltered L2, 128 dimensions, top 10, 4 KiB string data. Both handles
// read the same canonical collection; setup/backfill/write costs are not timed.
func BenchmarkSQLiteVectorProjections(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("records%d", size), func(b *testing.B) {
			ctx := context.Background()
			path := filepath.Join(b.TempDir(), "vectors.db")
			projected, err := Open(ctx, path, StoreOptions{VectorProjections: true})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { projected.Close() })
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 128, Metric: vectordata.DistanceL2}}}
			packed, err := projected.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				b.Fatal(err)
			}
			records := make([]vectordata.ModelRecord, size)
			for i := range records {
				vector := make([]float32, 128)
				for j := range vector {
					vector[j] = float32((i+j)%7) / 7
				}
				records[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Body": strings.Repeat("x", 4096)}, Vectors: map[string][]float32{"V": vector}}
			}
			if _, err := packed.Insert(ctx, records); err != nil {
				b.Fatal(err)
			}
			plain, err := Open(ctx, path, StoreOptions{})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { plain.Close() })
			canonical, err := plain.GetModelCollection("records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				b.Fatal(err)
			}
			query := records[0].Vectors["V"]
			want, err := canonical.Search(ctx, query, 10, vectordata.ModelSearchOptions{})
			if err != nil {
				b.Fatal(err)
			}
			if got, err := packed.Search(ctx, query, 10, vectordata.ModelSearchOptions{}); err != nil || !reflect.DeepEqual(got, want) {
				b.Fatalf("different page: %v", err)
			}
			for _, tc := range []struct {
				name       string
				collection vectordata.ModelCollection
			}{{"canonical", canonical}, {"packed", packed}} {
				b.Run(tc.name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						hits, err := tc.collection.Search(ctx, query, 10, vectordata.ModelSearchOptions{})
						if err != nil || len(hits) != 10 || hits[0].Record.Key != int64(0) || hits[0].Distance != 0 {
							b.Fatalf("invalid page: %+v %v", hits, err)
						}
					}
				})
			}
		})
	}
}
