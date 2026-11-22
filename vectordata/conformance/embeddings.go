package conformance

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func embeddingDefinition() vectordata.CollectionDefinition {
	return vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyString},
		Data:    []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}},
		Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, SourceProperty: "Body", Metric: vectordata.DistanceL2}},
	}
}

func embeddingRecord(key, body string, vector []float32) vectordata.ModelRecord {
	return vectordata.ModelRecord{Key: key, Data: map[string]any{"Body": body}, Vectors: map[string][]float32{"Vector": vector}}
}

// RunEmbeddingConfiguration checks store/collection/property defaults and batched
// write atomicity. The factory must return an isolated store configured with the
// supplied runtime defaults; run this in addition to Run for providers supporting
// store-level embedding configuration.
func RunEmbeddingConfiguration(t *testing.T, factory func(*testing.T, vectordata.ModelCollectionOptions) vectordata.ModelStore) {
	t.Helper()
	t.Run("search_criteria_during_embedding", func(t *testing.T) { runEmbeddedSearchCriteria(t, factory) })
	probe := factory(t, vectordata.ModelCollectionOptions{})
	if caps, ok := probe.(vectordata.CapabilityProvider); ok && caps.Capabilities().InputEmbedding {
		t.Run("input_embeddings", func(t *testing.T) { runInputEmbeddings(t, factory) })
		t.Run("output_representations", func(t *testing.T) { runOutputEmbeddings(t, factory) })
	}
	for _, tt := range []struct {
		name                                 string
		collectionEmbedder, propertyEmbedder bool
		collectionBatchSize                  int
		typedNil                             bool
	}{
		{name: "store_defaults"},
		{name: "collection_embedder", collectionEmbedder: true},
		{name: "property_embedder", collectionEmbedder: true, propertyEmbedder: true},
		{name: "collection_batch_size", collectionBatchSize: 1},
		{name: "typed_nil_fallback", typedNil: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			calls := map[string][][]string{}
			embedder := func(name string) vectordata.Embedder {
				return vectordata.EmbeddingFunc(func(_ context.Context, inputs []string) ([][]float32, error) {
					calls[name] = append(calls[name], append([]string(nil), inputs...))
					vectors := make([][]float32, len(inputs))
					for i, text := range inputs {
						vectors[i] = []float32{float32(len(text)), 1}
					}
					return vectors, nil
				})
			}
			store := factory(t, vectordata.ModelCollectionOptions{Embedder: embedder("store"), EmbeddingBatchSize: 2})
			d := embeddingDefinition()
			opts := vectordata.ModelCollectionOptions{EmbeddingBatchSize: tt.collectionBatchSize}
			selected := "store"
			if tt.collectionEmbedder {
				opts.Embedder = embedder("collection")
				selected = "collection"
			}
			if tt.propertyEmbedder {
				d.Vectors[0].Embedder = embedder("property")
				selected = "property"
			}
			if tt.typedNil {
				opts.Embedder = vectordata.EmbeddingFunc(nil)
				d.Vectors[0].Embedder = vectordata.EmbeddingFunc(nil)
			}
			c, err := store.EnsureModelCollection(ctx, "embedding_defaults", d, opts)
			if err != nil {
				t.Fatal(err)
			}
			inputs := []vectordata.ModelRecord{
				embeddingRecord("a", "alpha", nil), embeddingRecord("b", "explicit", []float32{99, 1}),
				embeddingRecord("c", "cat", nil), embeddingRecord("d", "door", nil),
				embeddingRecord("e", "e", nil), embeddingRecord("f", "flower", nil),
			}
			if keys, err := c.Insert(ctx, inputs); err != nil || len(keys) != len(inputs) {
				t.Fatalf("insert: %v, %v", keys, err)
			}
			want := [][]string{{"alpha", "cat"}, {"door", "e"}, {"flower"}}
			if tt.collectionBatchSize == 1 {
				want = [][]string{{"alpha"}, {"cat"}, {"door"}, {"e"}, {"flower"}}
			}
			if !reflect.DeepEqual(calls, map[string][][]string{selected: want}) {
				t.Fatalf("embedding calls: %v, want %s: %v", calls, selected, want)
			}
			for _, input := range inputs {
				row, err := c.Get(ctx, input.Key, vectordata.RetrievalOptions{IncludeVectors: true})
				if err != nil {
					t.Fatal(err)
				}
				value := float32(len(input.Data["Body"].(string)))
				if input.Key == "b" {
					value = 99
				}
				if !reflect.DeepEqual(row.Vectors["Vector"], []float32{value, 1}) {
					t.Fatalf("vector for %v: %v", input.Key, row.Vectors)
				}
				if input.Key != "b" && input.Vectors["Vector"] != nil {
					t.Fatal("write mutated input vectors")
				}
			}
			calls = map[string][][]string{}
			if rows, err := c.SearchText(ctx, "alpha", 1, vectordata.ModelSearchOptions{}); err != nil || len(rows) != 1 {
				t.Fatalf("text search: %v, %v", rows, err)
			}
			if !reflect.DeepEqual(calls, map[string][][]string{selected: {{"alpha"}}}) {
				t.Fatalf("query embedder: %v", calls)
			}
			if provider, ok := store.(vectordata.CapabilityProvider); ok && provider.Capabilities().HybridSearch {
				calls = map[string][][]string{}
				rows, err := c.(vectordata.HybridSearcher).HybridSearchText(ctx, "alpha", "alpha", 1, vectordata.HybridSearchOptions{})
				if err != nil || len(rows) != 1 || !reflect.DeepEqual(calls, map[string][][]string{selected: {{"alpha"}}}) {
					t.Fatalf("hybrid query embedding: %v, %v, calls=%v", rows, err, calls)
				}
			}
			// Runtime defaults and overrides must not become registered schema changes.
			if _, err := store.EnsureModelCollection(ctx, c.Name(), embeddingDefinition(), vectordata.ModelCollectionOptions{}); err != nil {
				t.Fatalf("runtime settings changed registration: %v", err)
			}
		})
	}
	for _, operation := range []string{"insert", "upsert"} {
		for _, failure := range []string{"error", "count", "dimension", "nonfinite", "cancel", "already_canceled"} {
			t.Run(operation+"_"+failure, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				providerError := errors.New("embedding service failed")
				embedder := vectordata.EmbeddingFunc(func(_ context.Context, inputs []string) ([][]float32, error) {
					calls++
					if failure == "cancel" {
						cancel()
					} // Deliberately return success despite cancellation.
					vectors := make([][]float32, len(inputs))
					for i := range vectors {
						vectors[i] = []float32{1, 1}
					}
					if calls == 2 {
						switch failure {
						case "error":
							return nil, providerError
						case "count":
							return vectors[:1], nil
						case "dimension":
							vectors[0] = []float32{1}
						case "nonfinite":
							vectors[0][0] = float32(math.NaN())
						}
					}
					return vectors, nil
				})
				store := factory(t, vectordata.ModelCollectionOptions{Embedder: embedder, EmbeddingBatchSize: 2})
				c, err := store.EnsureModelCollection(ctx, "embedding_failure", embeddingDefinition(), vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Insert(ctx, []vectordata.ModelRecord{embeddingRecord("seed", "original", []float32{7, 1})}); err != nil {
					t.Fatal(err)
				}
				inputs := []vectordata.ModelRecord{embeddingRecord("a", "a", nil), embeddingRecord("b", "b", nil), embeddingRecord("c", "c", nil), embeddingRecord("d", "d", nil), embeddingRecord("e", "e", nil), embeddingRecord("f", "f", nil)}
				if operation == "upsert" {
					inputs[0].Key = "seed"
				}
				if failure == "already_canceled" {
					cancel()
				}
				var keys []any
				if operation == "insert" {
					keys, err = c.Insert(ctx, inputs)
				} else {
					keys, err = c.Upsert(ctx, inputs)
				}
				wantCalls := 2
				wantError := vectordata.ErrInvalidVector
				switch failure {
				case "error":
					wantError = providerError
				case "dimension":
					wantError = vectordata.ErrDimensionMismatch
				case "cancel":
					wantError = context.Canceled
					wantCalls = 1
				case "already_canceled":
					wantError = context.Canceled
					wantCalls = 0
				}
				if !errors.Is(err, wantError) || len(keys) != 0 || calls != wantCalls {
					t.Fatalf("failed write: keys=%v, error=%v, calls=%d", keys, err, calls)
				}
				check := context.Background()
				if count, err := c.Count(check, nil); err != nil || count != 1 {
					t.Fatalf("partial write: count=%d, error=%v", count, err)
				}
				row, err := c.Get(check, "seed", vectordata.RetrievalOptions{IncludeVectors: true})
				if err != nil || row.Data["Body"] != "original" || !reflect.DeepEqual(row.Vectors["Vector"], []float32{7, 1}) {
					t.Fatalf("seed changed: %v, %v", row, err)
				}
			})
		}
	}
}

func runEmbeddedSearchCriteria(t *testing.T, factory func(*testing.T, vectordata.ModelCollectionOptions) vectordata.ModelStore) {
	ctx := t.Context()
	var mutate func()
	calls := 0
	store := factory(t, vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
		calls++
		mutate()
		return [][]float32{{1, 1}}, nil
	})})
	c, err := store.EnsureModelCollection(ctx, "search_criteria", embeddingDefinition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Insert(ctx, []vectordata.ModelRecord{embeddingRecord("a", "alpha", []float32{1, 1}), embeddingRecord("b", "beta", []float32{2, 1})}); err != nil {
		t.Fatal(err)
	}
	for _, criterion := range []string{"filter", "distance", "score"} {
		t.Run(criterion, func(t *testing.T) {
			opts := vectordata.ModelSearchOptions{}
			wantNext := []string{"a", "b"}
			switch criterion {
			case "filter":
				values := []any{"a"}
				opts.Filter = vectordata.InFilter{Field: vectordata.Column("ID"), Values: values}
				mutate = func() { values[0] = "b" }
				wantNext = []string{"b"}
			case "distance":
				threshold := 0.0
				opts.DistanceThreshold = &threshold
				mutate = func() { threshold = 2 }
			case "score":
				threshold := 1.0
				opts.ScoreThreshold = &threshold
				mutate = func() { threshold = 0 }
			}
			before := calls
			seq := vectordata.SearchTextIter(ctx, c, "query", 2, opts)
			if calls != before {
				t.Fatal("iterator embedded before enumeration")
			}
			for i, want := range [][]string{{"a"}, wantNext} {
				var keys []string
				for hit, err := range seq {
					if err != nil {
						t.Fatal(err)
					}
					keys = append(keys, hit.Record.Key.(string))
				}
				if !reflect.DeepEqual(keys, want) || calls != before+i+1 {
					t.Fatalf("enumeration %d: got keys %v, want %v; embedding calls %d", i, keys, want, calls-before)
				}
			}
		})
	}
}
