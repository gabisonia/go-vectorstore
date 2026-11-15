package conformance

import (
	"context"
	"math"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func runL1(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !slices.Contains(caps.DistanceMetrics, vectordata.DistanceL1) {
		t.Skip("L1 is not advertised")
	}
	for _, precision := range caps.VectorPrecisions {
		t.Run(string(precision), func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 3, Precision: precision, Metric: vectordata.DistanceL1, SourceProperty: "Body"}}}
			embedder := vectordata.EmbeddingFunc(func(_ context.Context, texts []string) ([][]float32, error) {
				out := make([][]float32, len(texts))
				for i := range out {
					out[i] = []float32{0, 0, 0}
				}
				return out, nil
			})
			c, err := store.EnsureModelCollection(ctx, "l1", d, vectordata.ModelCollectionOptions{Embedder: embedder})
			if err != nil {
				t.Fatal(err)
			}
			records := []vectordata.ModelRecord{
				{Key: "a", Data: map[string]any{"Body": "far"}, Vectors: map[string][]float32{"Vector": {-1, 2, -3}}},
				{Key: "b", Data: map[string]any{"Body": "near"}, Vectors: map[string][]float32{"Vector": {1, 1, 1}}},
				{Key: "c", Data: map[string]any{"Body": "origin"}, Vectors: map[string][]float32{"Vector": {0, 0, 0}}},
				{Key: "d", Data: map[string]any{"Body": "near"}, Vectors: map[string][]float32{"Vector": {0.1, 0.2, 0.3}}},
			}
			if _, err := c.Insert(ctx, records); err != nil {
				t.Fatal(err)
			}
			query := []float32{0, 0, 0}
			matches, err := c.Search(ctx, query, 4, vectordata.ModelSearchOptions{IncludeVectors: true})
			if err != nil || len(matches) != 4 {
				t.Fatalf("L1 search: %v, %v", matches, err)
			}
			wantKeys := []string{"c", "d", "b", "a"}
			wantDistances := []float64{0, 0.6, 3, 6}
			if precision == vectordata.VectorFloat16 {
				wantDistances[1] = 0.5999755859375
			}
			for i, match := range matches {
				if match.Record.Key != wantKeys[i] || math.Abs(match.Distance-wantDistances[i]) > 1e-6 || math.Abs(match.Score-1/(1+wantDistances[i])) > 1e-6 || match.ScoreKind != vectordata.ScoreSimilarity {
					t.Fatalf("L1 result %d: %+v", i, match)
				}
			}
			distance, score := 3.0, 0.5
			for _, tt := range []struct {
				opts vectordata.ModelSearchOptions
				keys []string
			}{
				{vectordata.ModelSearchOptions{DistanceThreshold: &distance}, []string{"c", "d", "b"}},
				{vectordata.ModelSearchOptions{ScoreThreshold: &score}, []string{"c", "d"}},
				{vectordata.ModelSearchOptions{Skip: 1}, []string{"d", "b", "a"}},
				{vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("Body"), "near")}, []string{"d", "b"}},
			} {
				got, err := c.Search(ctx, query, 4, tt.opts)
				if err != nil {
					t.Fatal(err)
				}
				keys := make([]string, len(got))
				for i := range got {
					keys[i] = got[i].Record.Key.(string)
				}
				if !slices.Equal(keys, tt.keys) {
					t.Fatalf("L1 paging/filter/threshold: got=%v want=%v", keys, tt.keys)
				}
			}
			text, err := c.SearchText(ctx, "query", 4, vectordata.ModelSearchOptions{})
			if err != nil || len(text) != 4 || text[0].Record.Key != "c" || text[0].Distance != 0 {
				t.Fatalf("L1 text search: %v, %v", text, err)
			}
			if caps.HybridSearch {
				got, err := c.(vectordata.HybridSearcher).HybridSearchText(ctx, "query", "near", 4, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("Body"), "near")}})
				if err != nil || len(got) != 2 {
					t.Fatalf("L1 hybrid search: %v, %v", got, err)
				}
				want := map[any]float64{"d": wantDistances[1], "b": 3}
				for _, row := range got {
					distance, ok := want[row.Record.Key]
					if !ok || row.ScoreKind != vectordata.ScoreRRF || math.Abs(row.Distance-distance) > 1e-6 {
						t.Fatalf("L1 hybrid distance: %+v", row)
					}
					delete(want, row.Record.Key)
				}
			}
		})
	}
}
