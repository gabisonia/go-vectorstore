package memory

import (
	"context"
	"iter"

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
	return c.searchRecords(ctx, top, opts, p.Metric, func(r vectordata.ModelRecord) float64 {
		n, _ := vectordata.SparseDistance(v, r.SparseVectors[p.Name], p.Metric)
		return float64(n)
	})
}
func (c *Collection) SearchSparseIter(ctx context.Context, v vectordata.SparseVector, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return func(yield func(vectordata.ModelSearchResult, error) bool) {
		rows, err := c.SearchSparse(ctx, v, top, opts)
		if err != nil {
			yield(vectordata.ModelSearchResult{}, err)
			return
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				yield(vectordata.ModelSearchResult{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
	}
}
