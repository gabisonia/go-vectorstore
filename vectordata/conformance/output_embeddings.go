package conformance

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func runOutputEmbeddings(t *testing.T, factory func(*testing.T, vectordata.ModelCollectionOptions) vectordata.ModelStore) {
	t.Run("bytes_input", func(t *testing.T) {
		adapter, err := vectordata.AdaptOutputEmbedder(vectordata.OutputEmbeddingFunc[[]byte, vectordata.Float16Bits](func(context.Context, [][]byte) ([][]vectordata.Float16Bits, error) {
			return [][]vectordata.Float16Bits{{0x3c00, 0x3800}}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		s := factory(t, vectordata.ModelCollectionOptions{InputEmbedder: adapter})
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeBytes}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2, SourceProperty: "Body", Metric: vectordata.DistanceL2, Precision: vectordata.VectorFloat16}}}
		c, err := s.EnsureModelCollection(t.Context(), "output_bytes", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Insert(t.Context(), []vectordata.ModelRecord{{Key: "bytes", Data: map[string]any{"Body": []byte{1, 2}}}}); err != nil {
			t.Fatal(err)
		}
		hits, err := c.(vectordata.InputSearcher).SearchInput(t.Context(), []byte{1, 2}, 1, vectordata.ModelSearchOptions{})
		if err != nil || len(hits) != 1 || hits[0].Record.Key != "bytes" || hits[0].Distance != 0 {
			t.Fatalf("half output for custom input: %v %v", hits, err)
		}
	})
	for _, precision := range []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16} {
		for _, level := range []string{"store", "collection", "property"} {
			t.Run(string(precision)+"_"+level, func(t *testing.T) {
				var calls []string
				buffer := make([]vectordata.Float16Bits, 2)
				source := vectordata.OutputEmbeddingFunc[string, vectordata.Float16Bits](func(ctx context.Context, inputs []string) ([][]vectordata.Float16Bits, error) {
					if len(inputs) != 1 {
						return nil, errors.New("expected one-input batching")
					}
					calls = append(calls, inputs[0])
					buffer[0], buffer[1] = 0x3c00, 0x3800
					if inputs[0] == "two" {
						buffer[0], buffer[1] = 0x4000, 0x3c00
					}
					if inputs[0] == "invalid" {
						buffer[0] = 0x7c00
					}
					return [][]vectordata.Float16Bits{buffer}, nil
				})
				adapter, err := vectordata.AdaptOutputEmbedder(source)
				if err != nil {
					t.Fatal(err)
				}
				wrong := vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
					return nil, errors.New("wrong generator selected")
				})
				defaults := vectordata.ModelCollectionOptions{EmbeddingBatchSize: 1}
				opts := vectordata.ModelCollectionOptions{}
				d := embeddingDefinition()
				d.Vectors[0].Precision = precision
				switch level {
				case "store":
					defaults.InputEmbedder = adapter
				case "collection":
					defaults.Embedder, opts.InputEmbedder = wrong, adapter
				case "property":
					defaults.Embedder, opts.Embedder, d.Vectors[0].InputEmbedder = wrong, wrong, adapter
				}
				s := factory(t, defaults)
				c, err := s.EnsureModelCollection(t.Context(), "output_embeddings", d, opts)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Insert(t.Context(), []vectordata.ModelRecord{embeddingRecord("one", "one", nil), embeddingRecord("two", "two", nil), embeddingRecord("explicit", "explicit", []float32{3, 0})}); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(calls, []string{"one", "two"}) {
					t.Fatal("batching, precedence or explicit-vector bypass:", calls)
				}
				for key, want := range map[string][]float32{"one": {1, 0.5}, "two": {2, 1}, "explicit": {3, 0}} {
					row, err := c.Get(t.Context(), key, vectordata.RetrievalOptions{IncludeVectors: true})
					if err != nil || !reflect.DeepEqual(row.Vectors["Vector"], want) {
						t.Fatalf("%s: %+v %v", key, row, err)
					}
				}
				hits, err := c.SearchText(t.Context(), "one", 1, vectordata.ModelSearchOptions{})
				if err != nil || len(hits) != 1 || hits[0].Record.Key != "one" || hits[0].Distance != 0 {
					t.Fatalf("typed-output query: %v %v", hits, err)
				}
				keys, err := c.Upsert(t.Context(), []vectordata.ModelRecord{embeddingRecord("one", "two", nil), embeddingRecord("bad", "invalid", nil)})
				if keys != nil || !errors.Is(err, vectordata.ErrInvalidVector) {
					t.Fatalf("invalid later batch accepted: %v %v", keys, err)
				}
				row, err := c.Get(t.Context(), "one", vectordata.RetrievalOptions{IncludeVectors: true})
				if err != nil || row.Data["Body"] != "one" || !reflect.DeepEqual(row.Vectors["Vector"], []float32{1, 0.5}) {
					t.Fatalf("failed embedding partially wrote: %+v %v", row, err)
				}
				if count, err := c.Count(t.Context(), nil); err != nil || count != 3 {
					t.Fatalf("failed embedding changed count: %d %v", count, err)
				}
				if !reflect.DeepEqual(calls, []string{"one", "two", "one", "two", "invalid"}) {
					t.Fatal("unexpected generation/re-embedding:", calls)
				}
			})
		}
	}
}
