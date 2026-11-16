package memory

import (
	"context"
	"iter"

	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

var _ vectordata.CollectionNameIterator = (*Store)(nil)
var _ vectordata.ModelBatchIterator = (*Collection)(nil)
var _ vectordata.TextSearchIterator = (*Collection)(nil)
var _ vectordata.InputSearchIterator = (*Collection)(nil)

func (s *Store) ListCollectionNamesIter(ctx context.Context) iter.Seq2[string, error] {
	return iteration.FromSlice(ctx, func() ([]string, error) { return s.ListCollectionNames(ctx) })
}
func (c *Collection) GetManyIter(ctx context.Context, keys []any, opts vectordata.RetrievalOptions) iter.Seq2[vectordata.ModelRecord, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelRecord, error) { return c.GetMany(ctx, keys, opts) })
}
func (c *Collection) SearchTextIter(ctx context.Context, text string, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.SearchInputIter(ctx, text, top, opts)
}
func (c *Collection) SearchInputIter(ctx context.Context, input any, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelSearchResult, error) { return c.SearchInput(ctx, input, top, opts) })
}
