package conformance

import (
	"context"
	"math"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func runL2Squared(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !slices.Contains(caps.DistanceMetrics, vectordata.DistanceL2Squared) {
		t.Skip("squared L2 is not advertised")
	}
	for _, precision := range caps.VectorPrecisions {
		t.Run(string(precision), func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			d := vectordata.CollectionDefinition{
				Key:     vectordata.KeyProperty{Name: "ID"},
				Data:    []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, Optional: true, IsFullTextIndexed: caps.FullTextIndexing}},
				Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Precision: precision, Metric: vectordata.DistanceL2Squared, SourceProperty: "Body", Optional: true}},
			}
			embedder := vectordata.EmbeddingFunc(func(_ context.Context, texts []string) ([][]float32, error) {
				out := make([][]float32, len(texts))
				for i := range out {
					out[i] = []float32{0, 0}
				}
				return out, nil
			})
			c, err := store.EnsureModelCollection(ctx, "squared", d, vectordata.ModelCollectionOptions{Embedder: embedder})
			if err != nil {
				t.Fatal(err)
			}
			records := []vectordata.ModelRecord{
				{Key: "a", Data: map[string]any{"Body": "origin"}, Vectors: map[string][]float32{"Vector": {0, 0}}},
				{Key: "b", Data: map[string]any{"Body": "near"}, Vectors: map[string][]float32{"Vector": {3, 4}}},
				{Key: "c", Data: map[string]any{"Body": "far"}, Vectors: map[string][]float32{"Vector": {6, 8}}},
				{Key: "missing", Data: map[string]any{"Body": nil}},
			}
			if _, err := c.Insert(ctx, records); err != nil {
				t.Fatal(err)
			}
			query := []float32{0, 0}
			hits, err := c.Search(ctx, query, 4, vectordata.ModelSearchOptions{IncludeVectors: true})
			if err != nil || len(hits) != 3 {
				t.Fatalf("squared search: %v, %v", hits, err)
			}
			for i, want := range []float64{0, 25, 100} {
				if hits[i].Record.Key != []string{"a", "b", "c"}[i] || math.Abs(hits[i].Distance-want) > 1e-6 || math.Abs(hits[i].Score-1/(1+want)) > 1e-12 || hits[i].ScoreKind != vectordata.ScoreSimilarity || len(hits[i].Record.Vectors["Vector"]) != 2 {
					t.Fatalf("squared distance/score: %+v", hits[i])
				}
			}
			for _, tt := range []struct {
				name            string
				distance, score *float64
				skip            int
				keys            []string
			}{
				{name: "distance uses squared units", distance: floatPointer(5), keys: []string{"a"}},
				{name: "inclusive distance", distance: floatPointer(25), keys: []string{"a", "b"}},
				{name: "score uses squared distance", score: floatPointer(1.0 / 6), keys: []string{"a"}},
				{name: "inclusive score", score: floatPointer(1.0 / 26), keys: []string{"a", "b"}},
				{name: "negative distance", distance: floatPointer(-1), keys: []string{}},
				{name: "paging", skip: 1, keys: []string{"b", "c"}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					got, err := c.Search(ctx, query, 4, vectordata.ModelSearchOptions{DistanceThreshold: tt.distance, ScoreThreshold: tt.score, Skip: tt.skip})
					if err != nil {
						t.Fatal(err)
					}
					keys := []string{}
					for _, hit := range got {
						keys = append(keys, hit.Record.Key.(string))
						if hit.Record.Vectors != nil {
							t.Fatal("projection included vectors")
						}
					}
					if !slices.Equal(keys, tt.keys) {
						t.Fatalf("got %v, want %v", keys, tt.keys)
					}
				})
			}
			text, err := c.SearchText(ctx, "origin", 3, vectordata.ModelSearchOptions{})
			if err != nil || len(text) != 3 || text[1].Distance != 25 {
				t.Fatalf("squared text query: %v, %v", text, err)
			}
			seen := 0
			for hit, err := range vectordata.SearchTextIter(ctx, c, "origin", 3, vectordata.ModelSearchOptions{}) {
				if err != nil {
					t.Fatal(err)
				}
				if seen == 1 && hit.Distance != 25 {
					t.Fatalf("iterator returned L2 units: %v", hit)
				}
				seen++
			}
			if seen != 3 {
				t.Fatal("iterator result count")
			}
			if caps.HybridSearch {
				got, err := c.(vectordata.HybridSearcher).HybridSearchText(ctx, "origin", "near", 3, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{DistanceThreshold: floatPointer(25)}})
				if err != nil || len(got) != 2 {
					t.Fatalf("squared hybrid: %v, %v", got, err)
				}
				for _, hit := range got {
					want := map[any]float64{"a": 0, "b": 25}[hit.Record.Key]
					if math.Abs(hit.Distance-want) > 1e-6 || hit.ScoreKind != vectordata.ScoreRRF {
						t.Fatalf("hybrid units: %v", hit)
					}
				}
			}
			// Existing L2 retains its original units and score normalization.
			l2 := d.Clone()
			l2.Vectors[0].Metric = vectordata.DistanceL2
			old, err := store.EnsureModelCollection(ctx, "euclidean", l2, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := old.Insert(ctx, records[:3]); err != nil {
				t.Fatal(err)
			}
			linear, err := old.Search(ctx, query, 3, vectordata.ModelSearchOptions{})
			if err != nil || linear[1].Distance != 5 || linear[1].Score != 1.0/6 {
				t.Fatalf("existing L2 changed: %v, %v", linear, err)
			}
			fraction := []float32{0.1, 0.2}
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "fraction", Data: map[string]any{"Body": "fraction"}, Vectors: map[string][]float32{"Vector": fraction}}}); err != nil {
				t.Fatal(err)
			}
			rounded, err := vectordata.NormalizeModelVector(fraction, d.Vectors[0])
			if err != nil {
				t.Fatal(err)
			}
			want := float64(rounded[0])*float64(rounded[0]) + float64(rounded[1])*float64(rounded[1])
			got, err := c.Search(ctx, query, 1, vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column("ID"), "fraction")})
			if err != nil || len(got) != 1 || math.Abs(got[0].Distance-want) > 1e-6 {
				t.Fatalf("rounded squared distance: %v, %v", got, err)
			}
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "embedded", Data: map[string]any{"Body": "generate"}}}); err != nil {
				t.Fatal(err)
			}
			row, err := c.Get(ctx, "embedded", vectordata.RetrievalOptions{IncludeVectors: true})
			if err != nil || len(row.Vectors["Vector"]) != 2 || row.Vectors["Vector"][0] != 0 {
				t.Fatalf("squared automatic embedding: %v, %v", row, err)
			}
			if caps.MigrationPlanning {
				plan, err := store.(vectordata.MigrationPlanner).PlanMigration(ctx, c.Name(), d)
				if err != nil || plan.HasManualWork() || len(plan.Steps) != 0 || plan.Finalization != nil {
					t.Fatalf("squared migration no-op: %+v, %v", plan, err)
				}
			}
		})
	}
}
