package memory

import (
	"context"
	"iter"

	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

var _ vectordata.SparseSearcher = (*Collection)(nil)

func (c *Collection) SearchSparse(ctx context.Context, v vectordata.SparseVector, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return nil, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return nil, err
	}
	v, err = vectordata.NormalizeSparseVector(v, p)
	if err != nil {
		return nil, err
	}
	if opts.ProviderOptions != nil {
		return nil, vectordata.ErrUnsupported
	}
	return c.searchRecords(ctx, top, opts, p, func(r vectordata.ModelRecord) (float64, error) {
		return vectordata.SparseDistance(v, r.SparseVectors[p.Name], p.Metric)
	})
}
func (c *Collection) SearchSparseIter(ctx context.Context, v vectordata.SparseVector, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelSearchResult, error) {
		return c.SearchSparse(ctx, v, top, opts)
	})
}
