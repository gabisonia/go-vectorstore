package vectordata

import (
	"context"
	"iter"

	"github.com/gabisonia/go-vectorstore/internal/iteration"
)

// These optional interfaces extend model read operations without changing the
// original ModelStore, ModelCollection or search interfaces. Errors are yielded
// once, after any earlier results. Breaking enumeration must release resources.
// Enumerating a sequence again reruns the operation, including embedding calls.
// Callers must keep inputs unchanged while a sequence is being consumed.

// CollectionNameIterator optionally enumerates a store's collection names.
type CollectionNameIterator interface {
	ListCollectionNamesIter(context.Context) iter.Seq2[string, error]
}

// ModelBatchIterator omits missing keys, deduplicates keys and does not guarantee order.
type ModelBatchIterator interface {
	GetManyIter(context.Context, []any, RetrievalOptions) iter.Seq2[ModelRecord, error]
}

// TextSearchIterator optionally enumerates text-query results.
type TextSearchIterator interface {
	SearchTextIter(context.Context, string, int, ModelSearchOptions) iter.Seq2[ModelSearchResult, error]
}

// InputSearchIterator optionally enumerates results from a custom embedding input.
type InputSearchIterator interface {
	SearchInputIter(context.Context, any, int, ModelSearchOptions) iter.Seq2[ModelSearchResult, error]
}

// HybridSearchIterator optionally enumerates dense-vector and text hybrid queries.
type HybridSearchIterator interface {
	HybridSearchIter(context.Context, []float32, string, int, HybridSearchOptions) iter.Seq2[ModelSearchResult, error]
	HybridSearchTextIter(context.Context, string, string, int, HybridSearchOptions) iter.Seq2[ModelSearchResult, error]
}

// InputHybridSearchIterator optionally enumerates custom-input hybrid queries.
type InputHybridSearchIterator interface {
	HybridSearchInputIter(context.Context, any, string, int, HybridSearchOptions) iter.Seq2[ModelSearchResult, error]
}

// BinaryHybridSearchIterator optionally enumerates binary-vector hybrid queries.
type BinaryHybridSearchIterator interface {
	HybridSearchBinaryIter(context.Context, BinaryVector, string, int, HybridSearchOptions) iter.Seq2[ModelSearchResult, error]
}

// SparseHybridSearchIterator optionally enumerates sparse-vector hybrid queries.
type SparseHybridSearchIterator interface {
	HybridSearchSparseIter(context.Context, SparseVector, string, int, HybridSearchOptions) iter.Seq2[ModelSearchResult, error]
}

// ListCollectionNamesIter prefers native iteration and otherwise buffers the
// existing slice API. Neither path performs work until enumeration begins.
func ListCollectionNamesIter(ctx context.Context, store CollectionManager) iter.Seq2[string, error] {
	return iteration.WithContext(ctx, func(yield func(string, error) bool) {
		if isNilValue(store) {
			yield("", ErrSchemaMismatch)
			return
		}
		if native, ok := store.(CollectionNameIterator); ok {
			native.ListCollectionNamesIter(ctx)(yield)
			return
		}
		iteration.FromSlice(ctx, func() ([]string, error) { return store.ListCollectionNames(ctx) })(yield)
	})
}

// GetManyIter prefers native batch iteration and otherwise buffers GetMany.
func GetManyIter(ctx context.Context, c ModelCollection, keys []any, opts RetrievalOptions) iter.Seq2[ModelRecord, error] {
	return iteration.WithContext(ctx, func(yield func(ModelRecord, error) bool) {
		if isNilValue(c) {
			yield(ModelRecord{}, ErrSchemaMismatch)
			return
		}
		if native, ok := c.(ModelBatchIterator); ok {
			native.GetManyIter(ctx, keys, opts)(yield)
			return
		}
		iteration.FromSlice(ctx, func() ([]ModelRecord, error) { return c.GetMany(ctx, keys, opts) })(yield)
	})
}

// SearchTextIter prefers native iteration and otherwise buffers SearchText.
func SearchTextIter(ctx context.Context, c ModelCollection, text string, top int, opts ModelSearchOptions) iter.Seq2[ModelSearchResult, error] {
	return iteration.WithContext(ctx, func(yield func(ModelSearchResult, error) bool) {
		if isNilValue(c) {
			yield(ModelSearchResult{}, ErrSchemaMismatch)
			return
		}
		if native, ok := c.(TextSearchIterator); ok {
			native.SearchTextIter(ctx, text, top, opts)(yield)
			return
		}
		iteration.FromSlice(ctx, func() ([]ModelSearchResult, error) { return c.SearchText(ctx, text, top, opts) })(yield)
	})
}

// SearchInputIter prefers native iteration, with a buffered InputSearcher fallback.
func SearchInputIter(ctx context.Context, c ModelCollection, input any, top int, opts ModelSearchOptions) iter.Seq2[ModelSearchResult, error] {
	return iteration.WithContext(ctx, func(yield func(ModelSearchResult, error) bool) {
		if isNilValue(c) {
			yield(ModelSearchResult{}, ErrSchemaMismatch)
			return
		}
		if native, ok := c.(InputSearchIterator); ok {
			native.SearchInputIter(ctx, input, top, opts)(yield)
			return
		}
		if fallback, ok := c.(InputSearcher); ok {
			iteration.FromSlice(ctx, func() ([]ModelSearchResult, error) { return fallback.SearchInput(ctx, input, top, opts) })(yield)
			return
		}
		yield(ModelSearchResult{}, ErrUnsupported)
	})
}

// HybridSearchIter prefers native iteration, with a buffered HybridSearcher fallback.
func HybridSearchIter(ctx context.Context, c ModelCollection, vector []float32, keywords string, top int, opts HybridSearchOptions) iter.Seq2[ModelSearchResult, error] {
	return iteration.WithContext(ctx, func(yield func(ModelSearchResult, error) bool) {
		if isNilValue(c) {
			yield(ModelSearchResult{}, ErrSchemaMismatch)
			return
		}
		if native, ok := c.(HybridSearchIterator); ok {
			native.HybridSearchIter(ctx, vector, keywords, top, opts)(yield)
			return
		}
		if fallback, ok := c.(HybridSearcher); ok {
			iteration.FromSlice(ctx, func() ([]ModelSearchResult, error) { return fallback.HybridSearch(ctx, vector, keywords, top, opts) })(yield)
			return
		}
		yield(ModelSearchResult{}, ErrUnsupported)
	})
}

// HybridSearchTextIter embeds text lazily and prefers native hybrid iteration.
func HybridSearchTextIter(ctx context.Context, c ModelCollection, text, keywords string, top int, opts HybridSearchOptions) iter.Seq2[ModelSearchResult, error] {
	return iteration.WithContext(ctx, func(yield func(ModelSearchResult, error) bool) {
		if isNilValue(c) {
			yield(ModelSearchResult{}, ErrSchemaMismatch)
			return
		}
		if native, ok := c.(HybridSearchIterator); ok {
			native.HybridSearchTextIter(ctx, text, keywords, top, opts)(yield)
			return
		}
		if fallback, ok := c.(HybridSearcher); ok {
			iteration.FromSlice(ctx, func() ([]ModelSearchResult, error) { return fallback.HybridSearchText(ctx, text, keywords, top, opts) })(yield)
			return
		}
		yield(ModelSearchResult{}, ErrUnsupported)
	})
}

// HybridSearchInputIter embeds an input lazily and prefers native hybrid iteration.
func HybridSearchInputIter(ctx context.Context, c ModelCollection, input any, keywords string, top int, opts HybridSearchOptions) iter.Seq2[ModelSearchResult, error] {
	return iteration.WithContext(ctx, func(yield func(ModelSearchResult, error) bool) {
		if isNilValue(c) {
			yield(ModelSearchResult{}, ErrSchemaMismatch)
			return
		}
		if native, ok := c.(InputHybridSearchIterator); ok {
			native.HybridSearchInputIter(ctx, input, keywords, top, opts)(yield)
			return
		}
		if fallback, ok := c.(InputHybridSearcher); ok {
			iteration.FromSlice(ctx, func() ([]ModelSearchResult, error) {
				return fallback.HybridSearchInput(ctx, input, keywords, top, opts)
			})(yield)
			return
		}
		yield(ModelSearchResult{}, ErrUnsupported)
	})
}

// HybridSearchBinaryIter prefers native binary hybrid iteration with a slice fallback.
func HybridSearchBinaryIter(ctx context.Context, c ModelCollection, vector BinaryVector, keywords string, top int, opts HybridSearchOptions) iter.Seq2[ModelSearchResult, error] {
	return iteration.WithContext(ctx, func(yield func(ModelSearchResult, error) bool) {
		if isNilValue(c) {
			yield(ModelSearchResult{}, ErrSchemaMismatch)
			return
		}
		if native, ok := c.(BinaryHybridSearchIterator); ok {
			native.HybridSearchBinaryIter(ctx, vector, keywords, top, opts)(yield)
			return
		}
		if fallback, ok := c.(BinaryHybridSearcher); ok {
			iteration.FromSlice(ctx, func() ([]ModelSearchResult, error) {
				return fallback.HybridSearchBinary(ctx, vector, keywords, top, opts)
			})(yield)
			return
		}
		yield(ModelSearchResult{}, ErrUnsupported)
	})
}

// HybridSearchSparseIter prefers native sparse hybrid iteration with a slice fallback.
func HybridSearchSparseIter(ctx context.Context, c ModelCollection, vector SparseVector, keywords string, top int, opts HybridSearchOptions) iter.Seq2[ModelSearchResult, error] {
	return iteration.WithContext(ctx, func(yield func(ModelSearchResult, error) bool) {
		if isNilValue(c) {
			yield(ModelSearchResult{}, ErrSchemaMismatch)
			return
		}
		if native, ok := c.(SparseHybridSearchIterator); ok {
			native.HybridSearchSparseIter(ctx, vector, keywords, top, opts)(yield)
			return
		}
		if fallback, ok := c.(SparseHybridSearcher); ok {
			iteration.FromSlice(ctx, func() ([]ModelSearchResult, error) {
				return fallback.HybridSearchSparse(ctx, vector, keywords, top, opts)
			})(yield)
			return
		}
		yield(ModelSearchResult{}, ErrUnsupported)
	})
}

func (c *TypedModelCollection[K, T]) GetManyIter(ctx context.Context, keys []K, opts RetrievalOptions) iter.Seq2[T, error] {
	return iteration.WithContext(ctx, func(yield func(T, error) bool) {
		values := make([]any, len(keys))
		for i, key := range keys {
			values[i] = key
		}
		for record, err := range GetManyIter(ctx, c.base, values, opts) {
			var item T
			if err == nil {
				item, err = c.codec.Decode(record)
			}
			if !yield(item, err) || err != nil {
				return
			}
		}
	})
}

func (c *TypedModelCollection[K, T]) searchSequence(ctx context.Context, source iter.Seq2[ModelSearchResult, error]) iter.Seq2[TypedModelSearchResult[T], error] {
	return iteration.WithContext(ctx, func(yield func(TypedModelSearchResult[T], error) bool) {
		for result, err := range source {
			var item T
			if err == nil {
				item, err = c.codec.Decode(result.Record)
			}
			if !yield(TypedModelSearchResult[T]{item, result.Distance, result.Score, result.ScoreKind}, err) || err != nil {
				return
			}
		}
	})
}

func (c *TypedModelCollection[K, T]) SearchTextIter(ctx context.Context, text string, top int, opts ModelSearchOptions) iter.Seq2[TypedModelSearchResult[T], error] {
	return c.searchSequence(ctx, SearchTextIter(ctx, c.base, text, top, opts))
}
func (c *TypedModelCollection[K, T]) SearchInputIter(ctx context.Context, input any, top int, opts ModelSearchOptions) iter.Seq2[TypedModelSearchResult[T], error] {
	return c.searchSequence(ctx, SearchInputIter(ctx, c.base, input, top, opts))
}
func (c *TypedModelCollection[K, T]) HybridSearchIter(ctx context.Context, vector []float32, keywords string, top int, opts HybridSearchOptions) iter.Seq2[TypedModelSearchResult[T], error] {
	return c.searchSequence(ctx, HybridSearchIter(ctx, c.base, vector, keywords, top, opts))
}
func (c *TypedModelCollection[K, T]) HybridSearchTextIter(ctx context.Context, text, keywords string, top int, opts HybridSearchOptions) iter.Seq2[TypedModelSearchResult[T], error] {
	return c.searchSequence(ctx, HybridSearchTextIter(ctx, c.base, text, keywords, top, opts))
}
func (c *TypedModelCollection[K, T]) HybridSearchInputIter(ctx context.Context, input any, keywords string, top int, opts HybridSearchOptions) iter.Seq2[TypedModelSearchResult[T], error] {
	return c.searchSequence(ctx, HybridSearchInputIter(ctx, c.base, input, keywords, top, opts))
}
func (c *TypedModelCollection[K, T]) HybridSearchBinaryIter(ctx context.Context, vector BinaryVector, keywords string, top int, opts HybridSearchOptions) iter.Seq2[TypedModelSearchResult[T], error] {
	return c.searchSequence(ctx, HybridSearchBinaryIter(ctx, c.base, vector, keywords, top, opts))
}
func (c *TypedModelCollection[K, T]) HybridSearchSparseIter(ctx context.Context, vector SparseVector, keywords string, top int, opts HybridSearchOptions) iter.Seq2[TypedModelSearchResult[T], error] {
	return c.searchSequence(ctx, HybridSearchSparseIter(ctx, c.base, vector, keywords, top, opts))
}
