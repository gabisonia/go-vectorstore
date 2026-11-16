package postgres

import (
	"context"
	"iter"

	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

var _ vectordata.CollectionNameIterator = (*PostgresVectorStore)(nil)
var _ vectordata.ModelBatchIterator = (*ModelCollection)(nil)
var _ vectordata.TextSearchIterator = (*ModelCollection)(nil)
var _ vectordata.InputSearchIterator = (*ModelCollection)(nil)
var _ vectordata.HybridSearchIterator = (*ModelCollection)(nil)
var _ vectordata.InputHybridSearchIterator = (*ModelCollection)(nil)
var _ vectordata.BinaryHybridSearchIterator = (*ModelCollection)(nil)
var _ vectordata.SparseHybridSearchIterator = (*ModelCollection)(nil)

func (c *ModelCollection) GetManyIter(ctx context.Context, keys []any, opts vectordata.RetrievalOptions) iter.Seq2[vectordata.ModelRecord, error] {
	return iteration.WithContext(ctx, func(yield func(vectordata.ModelRecord, error) bool) {
		values := []any{}
		seen := map[any]bool{}
		for _, key := range keys {
			if err := ctx.Err(); err != nil {
				yield(vectordata.ModelRecord{}, c.operation("get_many", err))
				return
			}
			value, err := c.definition.NormalizeKey(key)
			if err != nil {
				yield(vectordata.ModelRecord{}, c.operation("get_many", err))
				return
			}
			if !seen[value] {
				seen[value] = true
				values = append(values, value)
			}
		}
		for start := 0; start < len(values); start += maxRowsPerStatement {
			chunk := values[start:min(start+maxRowsPerStatement, len(values))]
			for record, err := range c.QueryIter(ctx, vectordata.In(vectordata.Column(c.definition.Key.Name), chunk...), len(chunk), vectordata.QueryOptions{IncludeVectors: opts.IncludeVectors}) {
				if !yield(record, err) || err != nil {
					return
				}
			}
		}
	})
}

type modelSearchPlan func() (string, []any, vectordata.VectorProperty, error)

func (c *ModelCollection) plannedSearchIter(ctx context.Context, opts vectordata.ModelSearchOptions, fusion bool, plan modelSearchPlan) iter.Seq2[vectordata.ModelSearchResult, error] {
	return iteration.WithContext(ctx, func(yield func(vectordata.ModelSearchResult, error) bool) {
		sql, args, property, err := plan()
		if err != nil {
			yield(vectordata.ModelSearchResult{}, c.operation("search", err))
			return
		}
		c.runSearch(ctx, sql, args, property, opts, fusion, yield)
	})
}

func (c *ModelCollection) SearchTextIter(ctx context.Context, text string, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.SearchInputIter(ctx, text, top, opts)
}
func (c *ModelCollection) SearchInputIter(ctx context.Context, input any, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.plannedSearchIter(ctx, opts, false, func() (string, []any, vectordata.VectorProperty, error) {
		vector, err := c.embedSearchInput(ctx, input, top, opts)
		if err != nil {
			return "", nil, vectordata.VectorProperty{}, err
		}
		return c.searchPlan(vector, top, opts)
	})
}

func (c *ModelCollection) HybridSearchIter(ctx context.Context, vector []float32, keywords string, top int, opts vectordata.HybridSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.plannedSearchIter(ctx, opts.ModelSearchOptions, true, func() (string, []any, vectordata.VectorProperty, error) {
		return c.hybridPlan(vector, keywords, top, opts)
	})
}
func (c *ModelCollection) HybridSearchTextIter(ctx context.Context, text, keywords string, top int, opts vectordata.HybridSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.HybridSearchInputIter(ctx, text, keywords, top, opts)
}
func (c *ModelCollection) HybridSearchInputIter(ctx context.Context, input any, keywords string, top int, opts vectordata.HybridSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.plannedSearchIter(ctx, opts.ModelSearchOptions, true, func() (string, []any, vectordata.VectorProperty, error) {
		config, err := c.validateHybridOptions(keywords, top, opts)
		if err != nil {
			return "", nil, config.vector, err
		}
		vector, err := vectordata.EmbedInputQuery(ctx, input, config.vector, c.options)
		if err != nil {
			return "", nil, config.vector, c.operation("embed_query", err)
		}
		return c.hybridPlan(vector, keywords, top, opts)
	})
}
func (c *ModelCollection) HybridSearchBinaryIter(ctx context.Context, vector vectordata.BinaryVector, keywords string, top int, opts vectordata.HybridSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.plannedSearchIter(ctx, opts.ModelSearchOptions, true, func() (string, []any, vectordata.VectorProperty, error) {
		config, err := c.validateHybridOptions(keywords, top, opts)
		if err != nil {
			return "", nil, config.vector, err
		}
		vector, err := vectordata.NormalizeBinaryVector(vector, config.vector)
		if err != nil {
			return "", nil, config.vector, err
		}
		return c.hybridValuePlan(binaryWriteValue(vector), keywords, top, opts)
	})
}
func (c *ModelCollection) HybridSearchSparseIter(ctx context.Context, vector vectordata.SparseVector, keywords string, top int, opts vectordata.HybridSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.plannedSearchIter(ctx, opts.ModelSearchOptions, true, func() (string, []any, vectordata.VectorProperty, error) {
		config, err := c.validateHybridOptions(keywords, top, opts)
		if err != nil {
			return "", nil, config.vector, err
		}
		vector, err := vectordata.NormalizeSparseVector(vector, config.vector)
		if err != nil {
			return "", nil, config.vector, err
		}
		return c.hybridValuePlan(sparseLiteral(vector), keywords, top, opts)
	})
}
