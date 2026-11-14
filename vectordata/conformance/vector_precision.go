package conformance

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func runVectorPrecisions(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	for _, precision := range caps.VectorPrecisions {
		for _, metric := range caps.DistanceMetrics {
			t.Run(string(precision)+"_"+string(metric), func(t *testing.T) {
				ctx := context.Background()
				store := factory(t)
				d := vectordata.CollectionDefinition{
					Key:     vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyString},
					Data:    []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}},
					Vectors: []vectordata.VectorProperty{{Name: "Selected", StorageName: `selected"vector`, Dimensions: 3, Precision: precision, Metric: metric, SourceProperty: "Body"}, {Name: "Full", Dimensions: 3}},
				}
				input := []float32{0.1, 0.3333, -0.1234}
				want := []float32{0.1, 0.3333, -0.1234}
				if precision == vectordata.VectorFloat16 {
					want = []float32{0.0999755859375, 0.333251953125, -0.1234130859375}
				}
				embedder := vectordata.EmbeddingFunc(func(_ context.Context, texts []string) ([][]float32, error) {
					out := make([][]float32, len(texts))
					for i := range out {
						out[i] = append([]float32(nil), input...)
					}
					return out, nil
				})
				c, err := store.EnsureModelCollection(ctx, "precision", d, vectordata.ModelCollectionOptions{Embedder: embedder, EmbeddingBatchSize: 1})
				if err != nil {
					t.Fatal(err)
				}
				records := []vectordata.ModelRecord{
					{Key: "a", Data: map[string]any{"Body": "alpha"}, Vectors: map[string][]float32{"Selected": input, "Full": input}},
					{Key: "b", Data: map[string]any{"Body": "beta"}, Vectors: map[string][]float32{"Selected": {-0.1, -0.3333, 0.1234}, "Full": {-0.1, -0.3333, 0.1234}}},
				}
				if _, err := c.Insert(ctx, records); err != nil {
					t.Fatal(err)
				}
				row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
				if err != nil || !reflect.DeepEqual(row.Vectors["Selected"], want) || !reflect.DeepEqual(row.Vectors["Full"], input) {
					t.Fatalf("mixed precision row: %v, %v", row, err)
				}
				if !reflect.DeepEqual(input, []float32{0.1, 0.3333, -0.1234}) {
					t.Fatal("write mutated caller vector")
				}
				opts := vectordata.ModelSearchOptions{VectorProperty: "Selected", IncludeVectors: true}
				matches, err := c.Search(ctx, input, 1, opts)
				if err != nil || len(matches) != 1 || matches[0].Record.Key != "a" || !reflect.DeepEqual(matches[0].Record.Vectors["Selected"], want) {
					t.Fatalf("precision search: %v, %v", matches, err)
				}
				if metric == vectordata.DistanceL2 && matches[0].Distance != 0 {
					t.Fatalf("query was not rounded like storage: %g", matches[0].Distance)
				}
				if _, err := c.Upsert(ctx, []vectordata.ModelRecord{{Key: "embedded", Data: map[string]any{"Body": "alpha"}, Vectors: map[string][]float32{"Full": input}}}); err != nil {
					t.Fatal(err)
				}
				row, err = c.Get(ctx, "embedded", vectordata.RetrievalOptions{IncludeVectors: true})
				if err != nil || !reflect.DeepEqual(row.Vectors["Selected"], want) {
					t.Fatalf("embedded precision: %v, %v", row, err)
				}
				if matches, err := c.SearchText(ctx, "alpha", 1, opts); err != nil || len(matches) != 1 {
					t.Fatalf("text search: %v, %v", matches, err)
				}
				if caps.HybridSearch {
					matches, err := c.(vectordata.HybridSearcher).HybridSearchText(ctx, "alpha", "alpha", 1, vectordata.HybridSearchOptions{ModelSearchOptions: opts})
					if err != nil || len(matches) != 1 || !reflect.DeepEqual(matches[0].Record.Vectors["Selected"], want) {
						t.Fatalf("hybrid precision: %v, %v", matches, err)
					}
				}
				if precision == vectordata.VectorFloat16 {
					for _, bad := range [][]float32{{65520, 1, 1}, {float32(math.NaN()), 1, 1}} {
						if _, err := c.Search(ctx, bad, 1, opts); !errors.Is(err, vectordata.ErrInvalidVector) {
							t.Fatalf("invalid half query: %v", err)
						}
						batch := []vectordata.ModelRecord{{Key: "new", Data: map[string]any{"Body": "alpha"}, Vectors: map[string][]float32{"Selected": input, "Full": input}}, {Key: "a", Data: map[string]any{"Body": "changed"}, Vectors: map[string][]float32{"Selected": bad, "Full": input}}}
						if _, err := c.Upsert(ctx, batch); !errors.Is(err, vectordata.ErrInvalidVector) {
							t.Fatalf("invalid half batch: %v", err)
						}
						if count, err := c.Count(ctx, nil); err != nil || count != 3 {
							t.Fatalf("partial write: %d, %v", count, err)
						}
						row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
						if err != nil || row.Data["Body"] != "alpha" {
							t.Fatalf("overwrote existing record: %v, %v", row, err)
						}
					}
				}
			})
		}
	}
}
