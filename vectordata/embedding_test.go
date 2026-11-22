package vectordata_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestEmbeddingBatchBoundariesAndReusedBuffers(t *testing.T) {
	for _, tt := range []struct {
		name string
		size int
		want []int
	}{
		{"unbounded", 0, []int{6}}, {"single", 1, []int{1, 1, 1, 1, 1, 1}},
		{"divisible", 2, []int{2, 2, 2}}, {"remainder", 4, []int{4, 2}},
		{"larger_than_input", 8, []int{6}}, {"max_int", int(^uint(0) >> 1), []int{6}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := (vectordata.CollectionDefinition{
				Key:     vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyString},
				Data:    []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}},
				Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, SourceProperty: "Body", Metric: vectordata.DistanceL2}},
			}).Normalize()
			if err != nil {
				t.Fatal(err)
			}
			input := make([]vectordata.ModelRecord, 6)
			buffer := make([][]float32, 6)
			for i := range input {
				input[i] = vectordata.ModelRecord{Key: "abcdef"[i : i+1], Data: map[string]any{"Body": "abcdef"[i : i+1]}}
				buffer[i] = make([]float32, 2)
			}
			var sizes []int
			embedder := vectordata.EmbeddingFunc(func(_ context.Context, inputs []string) ([][]float32, error) {
				sizes = append(sizes, len(inputs))
				for i, text := range inputs {
					buffer[i][0] = float32(text[0])
					buffer[i][1] = 1
				}
				return buffer[:len(inputs)], nil
			})
			prepared, err := vectordata.PrepareModelRecords(context.Background(), d, vectordata.ModelCollectionOptions{Embedder: embedder, EmbeddingBatchSize: tt.size}, input)
			if err != nil || !reflect.DeepEqual(sizes, tt.want) {
				t.Fatalf("batch sizes=%v, error=%v", sizes, err)
			}
			for i, r := range prepared {
				if !reflect.DeepEqual(r.Vectors["Vector"], []float32{float32('a' + i), 1}) {
					t.Fatalf("reused output changed record %d: %v", i, r)
				}
				if input[i].Vectors != nil {
					t.Fatal("input records mutated")
				}
			}
		})
	}
}

func TestEmbeddingOptionsValidationInSharedHelpers(t *testing.T) {
	invalid := vectordata.ModelCollectionOptions{EmbeddingBatchSize: -1}
	if _, err := vectordata.PrepareModelRecords(context.Background(), vectordata.CollectionDefinition{}, invalid, nil); !errors.Is(err, vectordata.ErrInvalidEmbeddingOptions) {
		t.Fatalf("prepare options: %v", err)
	}
	if _, err := vectordata.EmbedQuery(context.Background(), "text", vectordata.VectorProperty{}, invalid); !errors.Is(err, vectordata.ErrInvalidEmbeddingOptions) {
		t.Fatalf("query options: %v", err)
	}
}

func TestEmbedQueryHonorsCancellationFromEmbedder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	opts := vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
		calls++
		cancel()
		return [][]float32{{1, 1}}, nil
	})}
	p := vectordata.VectorProperty{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2}
	if _, err := vectordata.EmbedQuery(ctx, "text", p, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation after embedding: %v", err)
	}
	if _, err := vectordata.EmbedQuery(ctx, "text", p, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation before embedding: %v", err)
	}
	if calls != 1 {
		t.Fatalf("called embedder after cancellation: %d", calls)
	}
}

func TestInvalidExplicitVectorPreventsEmbeddingAcrossPropertiesAndRecords(t *testing.T) {
	generated := vectordata.VectorProperty{Name: "Generated", Dimensions: 2, SourceProperty: "Text"}
	explicit := vectordata.VectorProperty{Name: "Explicit", Dimensions: 2}
	for _, properties := range [][]vectordata.VectorProperty{{generated, explicit}, {explicit, generated}} {
		t.Run(properties[0].Name+"_first", func(t *testing.T) {
			d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Text", Type: vectordata.TypeString}}, Vectors: properties}).Normalize()
			if err != nil {
				t.Fatal(err)
			}
			records := []vectordata.ModelRecord{
				{Key: "first", Data: map[string]any{"Text": "one"}, Vectors: map[string][]float32{"Explicit": {1, 0}}},
				{Key: "last", Data: map[string]any{"Text": "two"}, Vectors: map[string][]float32{"Explicit": {1}}},
			}
			opts := vectordata.ModelCollectionOptions{EmbeddingBatchSize: 1, Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) {
				t.Fatal("embedder called before batch validation completed")
				return nil, nil
			})}
			got, err := vectordata.PrepareModelRecords(t.Context(), d, opts, records)
			if !errors.Is(err, vectordata.ErrDimensionMismatch) || got != nil {
				t.Fatalf("prepared=%v err=%v", got, err)
			}
		})
	}
}
