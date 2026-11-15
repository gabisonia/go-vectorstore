package memory

import (
	"context"
	"iter"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

var _ vectordata.BinarySearcher = (*Collection)(nil)

func (c *Collection) SearchBinary(ctx context.Context, v vectordata.BinaryVector, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return nil, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return nil, err
	}
	v, err = vectordata.NormalizeBinaryVector(v, p)
	if err != nil {
		return nil, err
	}
	if opts.ProviderOptions != nil {
		return nil, vectordata.ErrUnsupported
	}
	return c.searchRecords(ctx, top, opts, p, func(r vectordata.ModelRecord) (float64, error) {
		n, err := vectordata.HammingDistance(v, r.BinaryVectors[p.Name])
		return float64(n), err
	})
}
func (c *Collection) SearchBinaryIter(ctx context.Context, v vectordata.BinaryVector, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return func(yield func(vectordata.ModelSearchResult, error) bool) {
		rows, err := c.SearchBinary(ctx, v, top, opts)
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
