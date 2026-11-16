package conformance

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// runInputEmbeddings verifies provider execution, not just the shared embedding
// helpers: typed/dynamic writes, precedence, query validation and atomic failure.
func runInputEmbeddings(t *testing.T, factory func(*testing.T, vectordata.ModelCollectionOptions) vectordata.ModelStore) {
	ctx := context.Background()
	for _, level := range []string{"store", "collection", "property", "typed_nil"} {
		t.Run(level, func(t *testing.T) {
			calls := map[string][]int{}
			embedder := func(name string) vectordata.InputEmbedder {
				return vectordata.InputEmbeddingFunc[[]byte](func(_ context.Context, values [][]byte) ([][]float32, error) {
					calls[name] = append(calls[name], len(values))
					out := make([][]float32, len(values))
					for i, v := range values {
						out[i] = []float32{float32(v[0]), 1}
					}
					return out, nil
				})
			}
			store := factory(t, vectordata.ModelCollectionOptions{InputEmbedder: embedder("store"), EmbeddingBatchSize: 2})
			if caps, ok := store.(vectordata.CapabilityProvider); !ok || !caps.Capabilities().InputEmbedding {
				t.Fatal("input embedding capability missing")
			}
			d := embeddingDefinition()
			d.Vectors[0].SourceProperty = ""
			opts, selected := vectordata.ModelCollectionOptions{}, "store"
			switch level {
			case "collection":
				opts.InputEmbedder, selected = embedder("collection"), "collection"
			case "property":
				opts.InputEmbedder = embedder("collection")
				d.Vectors[0].InputEmbedder, selected = embedder("property"), "property"
			case "typed_nil":
				opts.InputEmbedder = vectordata.InputEmbeddingFunc[[]byte](nil)
				d.Vectors[0].InputEmbedder = vectordata.InputEmbeddingFunc[[]byte](nil)
			}
			c, err := store.EnsureModelCollection(ctx, "input_embedding", d, opts)
			if err != nil {
				t.Fatal(err)
			}
			input := []vectordata.ModelRecord{
				{Key: "a", Data: map[string]any{"Body": "alpha"}, EmbeddingInputs: map[string]any{"Vector": []byte{1}}},
				{Key: "b", Data: map[string]any{"Body": "beta"}, EmbeddingInputs: map[string]any{"Vector": []byte{2}}},
				{Key: "c", Data: map[string]any{"Body": "gamma"}, EmbeddingInputs: map[string]any{"Vector": []byte{3}}},
				// Explicit vectors bypass even incompatible transient inputs.
				{Key: "d", Data: map[string]any{"Body": "delta"}, Vectors: map[string][]float32{"Vector": {9, 1}}, EmbeddingInputs: map[string]any{"Vector": "ignored"}},
			}
			if _, err := c.Upsert(ctx, input); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, map[string][]int{selected: {2, 1}}) {
				t.Fatalf("batch/precedence: %v", calls)
			}
			input[0].EmbeddingInputs["Vector"].([]byte)[0] = 99
			for i, r := range input {
				got, err := c.Get(ctx, r.Key, vectordata.RetrievalOptions{IncludeVectors: true})
				want := float32(i + 1)
				if i == 3 {
					want = 9
				}
				if err != nil || got.EmbeddingInputs != nil || got.Vectors["Vector"][0] != want {
					t.Fatalf("stored input/vector: %+v, %v", got, err)
				}
			}
			s, ok := c.(vectordata.InputSearcher)
			if !ok {
				t.Fatal("InputSearcher missing")
			}
			calls = map[string][]int{}
			hits, err := s.SearchInput(ctx, []byte{1}, 1, vectordata.ModelSearchOptions{})
			if err != nil || len(hits) != 1 || hits[0].Record.Key != "a" || !reflect.DeepEqual(calls, map[string][]int{selected: {1}}) {
				t.Fatalf("input search: %v, %v, calls=%v", hits, err, calls)
			}
			if store.(vectordata.CapabilityProvider).Capabilities().HybridSearch {
				hits, err := c.(vectordata.InputHybridSearcher).HybridSearchInput(ctx, []byte{1}, "alpha", 1, vectordata.HybridSearchOptions{})
				if err != nil || len(hits) != 1 || hits[0].Record.Key != "a" {
					t.Fatalf("hybrid input search: %v, %v", hits, err)
				}
			}
			calls = map[string][]int{}
			for _, bad := range []vectordata.ModelSearchOptions{{Skip: -1}, {VectorProperty: "missing"}, {Filter: vectordata.Eq(vectordata.Column("missing"), 1)}} {
				if _, err := s.SearchInput(ctx, []byte{1}, 1, bad); err == nil {
					t.Fatal("invalid search accepted")
				}
			}
			if len(calls) != 0 {
				t.Fatalf("invalid search called embedder: %v", calls)
			}
			// Runtime generators and transient inputs must not alter registered schema.
			d.Vectors[0].InputEmbedder = nil
			if _, err := store.EnsureModelCollection(ctx, c.Name(), d, vectordata.ModelCollectionOptions{}); err != nil {
				t.Fatalf("runtime schema drift: %v", err)
			}
			if planner, ok := store.(vectordata.MigrationPlanner); ok {
				d.Vectors[0].InputEmbedder = embedder("plan")
				plan, err := planner.PlanMigration(ctx, c.Name(), d)
				if err != nil || len(plan.Steps) != 0 || plan.Finalization != nil || plan.Desired.Vectors[0].InputEmbedder != nil {
					t.Fatalf("runtime generator persisted in plan: %+v, %v", plan, err)
				}
			}
		})
	}
	t.Run("typed_sources", func(t *testing.T) {
		type image struct {
			Width int
			Bytes []byte
		}
		type document struct {
			ID           string  `vectordata:"key"`
			Text         string  `vectordata:"vector,dimensions=2,metric=l2"`
			Image        image   `vectordata:"vector,input,dimensions=2,metric=l2"`
			Audio        []byte  `vectordata:"vector,input,optional,dimensions=2,metric=l2"`
			OptionalText *string `vectordata:"vector,dimensions=2,metric=l2"`
		}
		textCalls, imageCalls := 0, 0
		d, err := vectordata.DefinitionFor[document]()
		if err != nil {
			t.Fatal(err)
		}
		d.Vectors[0].Embedder = vectordata.EmbeddingFunc(func(_ context.Context, values []string) ([][]float32, error) {
			textCalls++
			return [][]float32{{float32(len(values[0])), 1}}, nil
		})
		d.Vectors[1].InputEmbedder = vectordata.InputEmbeddingFunc[image](func(_ context.Context, values []image) ([][]float32, error) {
			imageCalls++
			return [][]float32{{float32(values[0].Width), 1}}, nil
		})
		d.Vectors[3].Embedder = d.Vectors[0].Embedder
		store := factory(t, vectordata.ModelCollectionOptions{InputEmbedder: vectordata.InputEmbeddingFunc[[]byte](func(context.Context, [][]byte) ([][]float32, error) {
			t.Fatal("property override or absent optional input used the store embedder")
			return nil, nil
		})})
		c, err := vectordata.EnsureTypedModelCollection[string, document](ctx, store, "typed_inputs", &d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		optionalText := "optional"
		if _, err := c.Insert(ctx, []document{{ID: "a", Text: "alpha", Image: image{Width: 4, Bytes: []byte{1}}, OptionalText: &optionalText}}); err != nil {
			t.Fatal(err)
		}
		got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
		if err != nil || got.ID != "a" || got.Text != "" || got.Image.Bytes != nil || got.Image.Width != 0 || got.Audio != nil || got.OptionalText != nil {
			t.Fatalf("input fields should be zero on retrieval: %+v, %v", got, err)
		}
		if hits, err := c.SearchInput(ctx, image{Width: 4}, 1, vectordata.ModelSearchOptions{VectorProperty: "Image"}); err != nil || len(hits) != 1 {
			t.Fatalf("typed custom search: %v, %v", hits, err)
		}
		if hits, err := c.SearchText(ctx, "alpha", 1, vectordata.ModelSearchOptions{VectorProperty: "Text"}); err != nil || len(hits) != 1 {
			t.Fatalf("string field search: %v, %v", hits, err)
		}
		if textCalls != 3 || imageCalls != 2 {
			t.Fatalf("callbacks: text=%d, image=%d", textCalls, imageCalls)
		}
	})
	t.Run("text_convenience", func(t *testing.T) {
		calls := 0
		store := factory(t, vectordata.ModelCollectionOptions{InputEmbedder: vectordata.InputEmbeddingFunc[string](func(_ context.Context, values []string) ([][]float32, error) {
			calls++
			return [][]float32{{float32(len(values[0])), 1}}, nil
		})})
		d := embeddingDefinition()
		c, err := store.EnsureModelCollection(ctx, "input_text", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Upsert(ctx, []vectordata.ModelRecord{embeddingRecord("a", "alpha", nil)}); err != nil {
			t.Fatal(err)
		}
		hits, err := c.SearchText(ctx, "alpha", 1, vectordata.ModelSearchOptions{})
		if err != nil || len(hits) != 1 || hits[0].Record.Key != "a" || calls != 2 {
			t.Fatalf("input-backed text convenience: %v, %v, calls=%d", hits, err, calls)
		}
		if store.(vectordata.CapabilityProvider).Capabilities().HybridSearch {
			hits, err := c.(vectordata.HybridSearcher).HybridSearchText(ctx, "alpha", "alpha", 1, vectordata.HybridSearchOptions{})
			if err != nil || len(hits) != 1 || calls != 3 {
				t.Fatalf("input-backed hybrid text: %v, %v, calls=%d", hits, err, calls)
			}
		}
	})
	t.Run("native_source", func(t *testing.T) {
		store := factory(t, vectordata.ModelCollectionOptions{InputEmbedder: vectordata.InputEmbeddingFunc[[]byte](func(_ context.Context, values [][]byte) ([][]float32, error) {
			return [][]float32{{float32(values[0][0]), 1}}, nil
		})})
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Bytes", Type: vectordata.TypeBytes}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", SourceProperty: "Bytes", Dimensions: 2, Metric: vectordata.DistanceL2}}}
		c, err := store.EnsureModelCollection(ctx, "native_input", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Bytes": []byte{4}}}}); err != nil {
			t.Fatal(err)
		}
		got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
		if err != nil || got.Vectors["Vector"][0] != 4 {
			t.Fatalf("byte source embedding: %v, %v", got, err)
		}
	})
	for _, operation := range []string{"insert", "upsert"} {
		for _, failure := range []string{"error", "count", "dimension", "nonfinite", "type", "cancel"} {
			t.Run(operation+"_"+failure, func(t *testing.T) {
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				calls := 0
				cause := errors.New("input service failed")
				store := factory(t, vectordata.ModelCollectionOptions{EmbeddingBatchSize: 1, InputEmbedder: vectordata.InputEmbeddingFunc[[]byte](func(_ context.Context, values [][]byte) ([][]float32, error) {
					calls++
					if calls == 2 {
						switch failure {
						case "error":
							return nil, cause
						case "count":
							return nil, nil
						case "dimension":
							return [][]float32{{1}}, nil
						case "nonfinite":
							return [][]float32{{float32(math.Inf(1)), 1}}, nil
						case "cancel":
							cancel()
						}
					}
					return [][]float32{{float32(values[0][0]), 1}}, nil
				})})
				d := embeddingDefinition()
				d.Vectors[0].SourceProperty = ""
				c, err := store.EnsureModelCollection(ctx, "input_failure", d, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Insert(ctx, []vectordata.ModelRecord{embeddingRecord("seed", "original", []float32{7, 1})}); err != nil {
					t.Fatal(err)
				}
				rows := []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Body": "new"}, EmbeddingInputs: map[string]any{"Vector": []byte{1}}}, {Key: "b", Data: map[string]any{"Body": "new"}, EmbeddingInputs: map[string]any{"Vector": []byte{2}}}}
				if operation == "upsert" {
					rows[0].Key = "seed"
				}
				if failure == "type" {
					rows[1].EmbeddingInputs["Vector"] = "bad"
				}
				var keys []any
				if operation == "insert" {
					keys, err = c.Insert(ctx, rows)
				} else {
					keys, err = c.Upsert(ctx, rows)
				}
				wantErr := vectordata.ErrInvalidVector
				switch failure {
				case "error":
					wantErr = cause
				case "dimension":
					wantErr = vectordata.ErrDimensionMismatch
				case "type":
					wantErr = vectordata.ErrInvalidEmbeddingOptions
				case "cancel":
					wantErr = context.Canceled
				}
				if !errors.Is(err, wantErr) || len(keys) != 0 {
					t.Fatalf("failed batch: %v, %v", keys, err)
				}
				if count, err := c.Count(context.Background(), nil); err != nil || count != 1 {
					t.Fatalf("partial write: %d, %v", count, err)
				}
				got, err := c.Get(context.Background(), "seed", vectordata.RetrievalOptions{IncludeVectors: true})
				if err != nil || got.Data["Body"] != "original" || got.Vectors["Vector"][0] != 7 {
					t.Fatalf("seed changed: %v, %v", got, err)
				}
			})
		}
	}
}
