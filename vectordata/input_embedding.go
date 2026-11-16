package vectordata

import (
	"context"
	"fmt"
	"reflect"
)

// InputEmbedder generates one dense vector per input, preserving order. It may
// accept text, image/audio bytes, or application values. Inputs are borrowed:
// implementations must not mutate them or retain them after returning. They must
// honor ctx and be safe for concurrent use. Outputs may be reused on the next call.
type InputEmbedder interface {
	EmbedInputs(context.Context, []any) ([][]float32, error)
}

// InputEmbeddingFunc adapts a typed embedding function. Every input in a batch
// must be a non-nil T; type mismatches fail before invoking the function.
type InputEmbeddingFunc[T any] func(context.Context, []T) ([][]float32, error)

func (f InputEmbeddingFunc[T]) EmbedInputs(ctx context.Context, inputs []any) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil {
		return nil, fmt.Errorf("%w: nil input embedder", ErrUnsupported)
	}
	values := make([]T, len(inputs))
	for i, input := range inputs {
		value, ok := input.(T)
		if !ok || isNilValue(input) {
			return nil, fmt.Errorf("%w: embedding input %d must be a non-nil %s, got %T", ErrInvalidEmbeddingOptions, i, reflect.TypeFor[T](), input)
		}
		values[i] = value
	}
	return f(ctx, values)
}

// InputSearcher is optional so existing ModelCollection providers remain compatible.
// The input is converted to a dense vector using the selected property's embedder.
type InputSearcher interface {
	SearchInput(context.Context, any, int, ModelSearchOptions) ([]ModelSearchResult, error)
}

// InputHybridSearcher optionally combines an embedded input with keyword search.
type InputHybridSearcher interface {
	HybridSearchInput(context.Context, any, string, int, HybridSearchOptions) ([]ModelSearchResult, error)
}

func (c *TypedModelCollection[K, T]) SearchInput(ctx context.Context, input any, top int, opts ModelSearchOptions) ([]TypedModelSearchResult[T], error) {
	s, ok := c.base.(InputSearcher)
	if !ok {
		return nil, ErrUnsupported
	}
	return c.searchResults(s.SearchInput(ctx, input, top, opts))
}

func (c *TypedModelCollection[K, T]) HybridSearchInput(ctx context.Context, input any, keywords string, top int, opts HybridSearchOptions) ([]TypedModelSearchResult[T], error) {
	s, ok := c.base.(InputHybridSearcher)
	if !ok {
		return nil, ErrUnsupported
	}
	return c.searchResults(s.HybridSearchInput(ctx, input, keywords, top, opts))
}

// EmbedInputQuery embeds one non-nil input and validates the output. Text queries
// and writes use the same resolution: property, collection, then store defaults.
func EmbedInputQuery(ctx context.Context, input any, p VectorProperty, opts ModelCollectionOptions) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Kind == VectorBinary || p.Kind == VectorSparse {
		return nil, fmt.Errorf("%w: binary/sparse queries require explicit vector values", ErrUnsupported)
	}
	if _, err := opts.WithDefaults(ModelCollectionOptions{}); err != nil {
		return nil, err
	}
	vectors, err := embedInputs(ctx, []any{input}, p, opts)
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("%w: expected one query embedding", ErrInvalidVector)
	}
	vector, err := NormalizeModelVector(vectors[0], p)
	if err != nil {
		return nil, err
	}
	// Do not expose an embedder-owned buffer to the caller or a later request.
	return append([]float32(nil), vector...), nil
}

func embedInputs(ctx context.Context, inputs []any, p VectorProperty, opts ModelCollectionOptions) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !isNilValue(p.Embedder) && !isNilValue(p.InputEmbedder) {
		return nil, fmt.Errorf("%w: configure either Embedder or InputEmbedder per property", ErrInvalidEmbeddingOptions)
	}
	textEmbedder, inputEmbedder := p.Embedder, p.InputEmbedder
	if isNilValue(textEmbedder) && isNilValue(inputEmbedder) {
		textEmbedder, inputEmbedder = opts.Embedder, opts.InputEmbedder
	}
	for i, input := range inputs {
		if isNilValue(input) {
			return nil, fmt.Errorf("%w: embedding input %d is nil", ErrInvalidEmbeddingOptions, i)
		}
	}
	var vectors [][]float32
	var err error
	if !isNilValue(inputEmbedder) {
		vectors, err = inputEmbedder.EmbedInputs(ctx, inputs)
	} else if !isNilValue(textEmbedder) {
		texts := make([]string, len(inputs))
		for i, input := range inputs {
			value := reflect.ValueOf(input)
			if value.Kind() != reflect.String {
				return nil, fmt.Errorf("%w: Embedder requires text; configure InputEmbedder for %T", ErrInvalidEmbeddingOptions, input)
			}
			texts[i] = value.String()
		}
		vectors, err = textEmbedder.Embed(ctx, texts)
	} else {
		return nil, fmt.Errorf("%w: no embedder for %q", ErrUnsupported, p.Name)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return vectors, nil
}

// Compile-time check that the generic adapter satisfies the runtime contract.
var _ InputEmbedder = InputEmbeddingFunc[string](nil)
