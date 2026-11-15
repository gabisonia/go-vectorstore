package vectordata

import (
	"context"
	"fmt"
	"iter"
	"math"
)

const (
	VectorSparse            VectorKind = "sparse"
	MaxSparseDimensions                = 1_000_000_000
	MaxSparseNonZero                   = 16000
	MaxIndexedSparseNonZero            = 1000
)

// SparseVector stores zero-based, strictly increasing coordinates. Dimensions
// includes omitted zero components. NormalizeSparseVector removes explicit zeros.
type SparseVector struct {
	Dimensions int
	Indices    []int
	Values     []float32
}

func validateSparse(v SparseVector) error {
	if v.Dimensions < 1 || v.Dimensions > MaxSparseDimensions {
		return fmt.Errorf("%w: sparse dimensions must be 1–%d", ErrDimensionMismatch, MaxSparseDimensions)
	}
	if len(v.Indices) != len(v.Values) || len(v.Indices) > MaxSparseNonZero {
		return fmt.Errorf("%w: sparse coordinates require equal lengths and at most %d entries", ErrInvalidVector, MaxSparseNonZero)
	}
	for i, index := range v.Indices {
		if index < 0 || index >= v.Dimensions || i > 0 && index <= v.Indices[i-1] {
			return fmt.Errorf("%w: sparse indices must be increasing, unique and within dimensions", ErrInvalidVector)
		}
		if math.IsNaN(float64(v.Values[i])) || math.IsInf(float64(v.Values[i]), 0) {
			return fmt.Errorf("%w: sparse values must be finite", ErrInvalidVector)
		}
	}
	return nil
}

func cloneSparse(v SparseVector) SparseVector {
	v.Indices = append([]int(nil), v.Indices...)
	v.Values = append([]float32(nil), v.Values...)
	return v
}

// NewSparseVector validates and copies coordinates, omitting explicit zeros.
func NewSparseVector(dimensions int, indices []int, values []float32) (SparseVector, error) {
	v := SparseVector{Dimensions: dimensions, Indices: indices, Values: values}
	if err := validateSparse(v); err != nil {
		return SparseVector{}, err
	}
	out := SparseVector{Dimensions: dimensions}
	nonzero := 0
	for _, x := range values {
		if x != 0 {
			nonzero++
		}
	}
	if nonzero > 0 {
		out.Indices = make([]int, 0, nonzero)
		out.Values = make([]float32, 0, nonzero)
	}
	for i, x := range values {
		if x != 0 {
			out.Indices = append(out.Indices, indices[i])
			out.Values = append(out.Values, x)
		}
	}
	return out, nil
}

// NormalizeSparseVector validates the property, cosine norm and index entry
// limit, returning independent canonical coordinate slices.
func NormalizeSparseVector(v SparseVector, p VectorProperty) (SparseVector, error) {
	if p.Kind != VectorSparse {
		return SparseVector{}, fmt.Errorf("%w: property %q is not sparse", ErrInvalidVector, p.Name)
	}
	if v.Dimensions != p.Dimensions {
		return SparseVector{}, fmt.Errorf("%w: property %q expects %d dimensions, got %d", ErrDimensionMismatch, p.Name, p.Dimensions, v.Dimensions)
	}
	v, err := NewSparseVector(v.Dimensions, v.Indices, v.Values)
	if err != nil {
		return SparseVector{}, err
	}
	if (p.Metric == "" || p.Metric == DistanceCosine) && len(v.Values) == 0 {
		return SparseVector{}, fmt.Errorf("%w: zero cosine sparse vector", ErrInvalidVector)
	}
	if p.Index != nil && len(v.Values) > MaxIndexedSparseNonZero {
		return SparseVector{}, fmt.Errorf("%w: indexed sparse vectors support at most %d nonzero elements", ErrInvalidVector, MaxIndexedSparseNonZero)
	}
	return v, nil
}

// SparseDistance computes a distance in O(nnz(a)+nnz(b)) time and constant extra
// space, without expanding the declared dimension count.
func SparseDistance(a, b SparseVector, metric DistanceMetric) (float64, error) {
	if err := validateSparse(a); err != nil {
		return 0, err
	}
	if err := validateSparse(b); err != nil {
		return 0, err
	}
	if a.Dimensions != b.Dimensions {
		return 0, ErrDimensionMismatch
	}
	if metric == "" {
		metric = DistanceCosine
	}
	if metric != DistanceCosine && metric != DistanceL2 && metric != DistanceL1 && metric != DistanceInnerProduct {
		return 0, fmt.Errorf("%w: unsupported sparse metric", ErrSchemaMismatch)
	}
	var aa, bb, dot, sum, l1 float64
	for _, x := range a.Values {
		aa += float64(x) * float64(x)
	}
	for _, x := range b.Values {
		bb += float64(x) * float64(x)
	}
	i, j := 0, 0
	for i < len(a.Indices) || j < len(b.Indices) {
		var x, y float64
		if j == len(b.Indices) || i < len(a.Indices) && a.Indices[i] < b.Indices[j] {
			x = float64(a.Values[i])
			i++
		} else if i == len(a.Indices) || b.Indices[j] < a.Indices[i] {
			y = float64(b.Values[j])
			j++
		} else {
			x, y = float64(a.Values[i]), float64(b.Values[j])
			i++
			j++
		}
		delta := x - y
		sum += delta * delta
		l1 += math.Abs(delta)
		dot += x * y
	}
	switch metric {
	case DistanceL2:
		return math.Sqrt(sum), nil
	case DistanceL1:
		return l1, nil
	case DistanceInnerProduct:
		return -dot, nil
	default:
		if aa == 0 || bb == 0 {
			return 0, fmt.Errorf("%w: zero cosine sparse vector", ErrInvalidVector)
		}
		return 1 - math.Max(-1, math.Min(1, dot/(math.Sqrt(aa)*math.Sqrt(bb)))), nil
	}
}

// SparseSearcher is optional so existing ModelCollection providers stay valid.
type SparseSearcher interface {
	SearchSparse(context.Context, SparseVector, int, ModelSearchOptions) ([]ModelSearchResult, error)
	SearchSparseIter(context.Context, SparseVector, int, ModelSearchOptions) iter.Seq2[ModelSearchResult, error]
}

// SparseHybridSearcher optionally combines sparse similarity and keyword search.
type SparseHybridSearcher interface {
	HybridSearchSparse(context.Context, SparseVector, string, int, HybridSearchOptions) ([]ModelSearchResult, error)
}

func (c *TypedModelCollection[K, T]) SearchSparse(ctx context.Context, v SparseVector, top int, opts ModelSearchOptions) ([]TypedModelSearchResult[T], error) {
	s, ok := c.base.(SparseSearcher)
	if !ok {
		return nil, ErrUnsupported
	}
	return c.searchResults(s.SearchSparse(ctx, v, top, opts))
}
func (c *TypedModelCollection[K, T]) SearchSparseIter(ctx context.Context, v SparseVector, top int, opts ModelSearchOptions) iter.Seq2[TypedModelSearchResult[T], error] {
	return func(yield func(TypedModelSearchResult[T], error) bool) {
		s, ok := c.base.(SparseSearcher)
		if !ok {
			yield(TypedModelSearchResult[T]{}, ErrUnsupported)
			return
		}
		for r, err := range s.SearchSparseIter(ctx, v, top, opts) {
			var item T
			if err == nil {
				item, err = c.codec.Decode(r.Record)
			}
			if !yield(TypedModelSearchResult[T]{item, r.Distance, r.Score, r.ScoreKind}, err) || err != nil {
				return
			}
		}
	}
}
func (c *TypedModelCollection[K, T]) HybridSearchSparse(ctx context.Context, v SparseVector, keywords string, top int, opts HybridSearchOptions) ([]TypedModelSearchResult[T], error) {
	s, ok := c.base.(SparseHybridSearcher)
	if !ok {
		return nil, ErrUnsupported
	}
	return c.searchResults(s.HybridSearchSparse(ctx, v, keywords, top, opts))
}
