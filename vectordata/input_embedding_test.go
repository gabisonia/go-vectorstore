package vectordata_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestInputEmbeddingOptionsResolution(t *testing.T) {
	text := vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { return [][]float32{{1, 0}}, nil })
	input := vectordata.InputEmbeddingFunc[[]byte](func(context.Context, [][]byte) ([][]float32, error) { return [][]float32{{2, 0}}, nil })
	for _, tt := range []struct {
		name                         string
		local, defaults              vectordata.ModelCollectionOptions
		wantText, wantInput, invalid bool
	}{
		{"inherit_input", vectordata.ModelCollectionOptions{}, vectordata.ModelCollectionOptions{InputEmbedder: input}, false, true, false},
		{"text_over_input", vectordata.ModelCollectionOptions{Embedder: text}, vectordata.ModelCollectionOptions{InputEmbedder: input}, true, false, false},
		{"input_over_text", vectordata.ModelCollectionOptions{InputEmbedder: input}, vectordata.ModelCollectionOptions{Embedder: text}, false, true, false},
		{"typed_nil", vectordata.ModelCollectionOptions{InputEmbedder: vectordata.InputEmbeddingFunc[string](nil)}, vectordata.ModelCollectionOptions{InputEmbedder: input}, false, true, false},
		{"ambiguous_local", vectordata.ModelCollectionOptions{Embedder: text, InputEmbedder: input}, vectordata.ModelCollectionOptions{}, false, false, true},
		{"ambiguous_defaults", vectordata.ModelCollectionOptions{}, vectordata.ModelCollectionOptions{Embedder: text, InputEmbedder: input}, false, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := tt.local.WithDefaults(tt.defaults)
			if tt.invalid {
				if !errors.Is(err, vectordata.ErrInvalidEmbeddingOptions) {
					t.Fatalf("ambiguous options: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p := vectordata.VectorProperty{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2}
			// Verify the resolved runtime behavior, including cross-kind overrides.
			var value any = "text"
			want := float32(1)
			if tt.wantInput {
				value, want = []byte{1}, 2
			}
			v, err := vectordata.EmbedInputQuery(context.Background(), value, p, opts)
			if err != nil || v[0] != want {
				t.Fatalf("resolved embedding: %v, %v", v, err)
			}
		})
	}
}

func TestInputEmbeddingValidationAndCancellation(t *testing.T) {
	p := vectordata.VectorProperty{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2}
	for _, failure := range []string{"wrong_type", "nil", "typed_nil", "count", "dimension", "nonfinite", "error", "canceled_before", "canceled_during", "binary", "sparse"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			cause := errors.New("input embedding failed")
			opts := vectordata.ModelCollectionOptions{InputEmbedder: vectordata.InputEmbeddingFunc[[]byte](func(context.Context, [][]byte) ([][]float32, error) {
				calls++
				switch failure {
				case "count":
					return nil, nil
				case "dimension":
					return [][]float32{{1}}, nil
				case "nonfinite":
					return [][]float32{{float32(math.NaN()), 1}}, nil
				case "error":
					return nil, cause
				case "canceled_during":
					cancel()
				}
				return [][]float32{{1, 0}}, nil
			})}
			var value any = []byte{1}
			property, wantCalls, wantErr := p, 1, vectordata.ErrInvalidVector
			switch failure {
			case "wrong_type":
				value, wantCalls, wantErr = "text", 0, vectordata.ErrInvalidEmbeddingOptions
			case "nil":
				value, wantCalls, wantErr = nil, 0, vectordata.ErrInvalidEmbeddingOptions
			case "typed_nil":
				value, wantCalls, wantErr = []byte(nil), 0, vectordata.ErrInvalidEmbeddingOptions
			case "dimension":
				wantErr = vectordata.ErrDimensionMismatch
			case "error":
				wantErr = cause
			case "canceled_before":
				cancel()
				wantCalls, wantErr = 0, context.Canceled
			case "canceled_during":
				wantErr = context.Canceled
			case "binary":
				property.Kind, wantCalls, wantErr = vectordata.VectorBinary, 0, vectordata.ErrUnsupported
			case "sparse":
				property.Kind, wantCalls, wantErr = vectordata.VectorSparse, 0, vectordata.ErrUnsupported
			}
			_, err := vectordata.EmbedInputQuery(ctx, value, property, opts)
			if !errors.Is(err, wantErr) || calls != wantCalls {
				t.Fatalf("error=%v, calls=%d, want %v/%d", err, calls, wantErr, wantCalls)
			}
		})
	}
	// A bad type later in the same batch never invokes the typed callback.
	calls := 0
	f := vectordata.InputEmbeddingFunc[[]byte](func(context.Context, [][]byte) ([][]float32, error) { calls++; return nil, nil })
	if _, err := f.EmbedInputs(context.Background(), []any{[]byte{1}, "bad"}); !errors.Is(err, vectordata.ErrInvalidEmbeddingOptions) || calls != 0 {
		t.Fatalf("type check: %v, calls=%d", err, calls)
	}
}

func TestInputEmbeddingBatchBuffersAndTransientInputs(t *testing.T) {
	d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	records := []vectordata.ModelRecord{
		{Key: "a", EmbeddingInputs: map[string]any{"Vector": []byte{1}}},
		{Key: "b", EmbeddingInputs: map[string]any{"Vector": []byte{2}}},
		{Key: "c", EmbeddingInputs: map[string]any{"Vector": []byte{3}}},
	}
	buffer := [][]float32{{0, 1}, {0, 1}}
	var sizes []int
	opts := vectordata.ModelCollectionOptions{EmbeddingBatchSize: 2, InputEmbedder: vectordata.InputEmbeddingFunc[[]byte](func(_ context.Context, values [][]byte) ([][]float32, error) {
		sizes = append(sizes, len(values))
		for i, v := range values {
			buffer[i][0] = float32(v[0])
		}
		return buffer[:len(values)], nil
	})}
	prepared, err := vectordata.PrepareModelRecords(context.Background(), d, opts, records)
	if err != nil || !reflect.DeepEqual(sizes, []int{2, 1}) {
		t.Fatalf("batching: %v, %v", sizes, err)
	}
	buffer[0][0] = 99
	for i, r := range prepared {
		if r.EmbeddingInputs != nil || r.Vectors["Vector"][0] != float32(i+1) || records[i].Vectors != nil {
			t.Fatalf("record ownership: %#v", r)
		}
	}
	v, err := vectordata.EmbedInputQuery(context.Background(), []byte{4}, d.Vectors[0], opts)
	if err != nil {
		t.Fatal(err)
	}
	buffer[0][0] = 99
	if v[0] != 4 {
		t.Fatalf("query aliases embedder buffer: %v", v)
	}
}

func TestInputFieldSchemaValidation(t *testing.T) {
	type invalid struct {
		ID     string `vectordata:"key"`
		Vector []byte `vectordata:"vector,input,kind=binary,dimensions=8"`
	}
	if _, err := vectordata.NewStructCodec[invalid](nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("binary input: %v", err)
	}
	type ambiguous struct {
		ID     string `vectordata:"key"`
		Body   string `vectordata:"data"`
		Vector string `vectordata:"vector,source=Body,dimensions=2"`
	}
	if _, err := vectordata.NewStructCodec[ambiguous](nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("two input sources: %v", err)
	}
}
