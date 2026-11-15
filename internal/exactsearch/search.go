// Package exactsearch ranks canonical records without retaining every match.
package exactsearch

import (
	"cmp"
	"container/heap"
	"context"
	"iter"
	"math"
	"sort"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type Measure func(vectordata.ModelRecord) (float64, error)

// Run consumes a stable record snapshot. Sources may lend immutable memory
// records or yield owned decoded records. Only the returned page is deep-copied.
func Run(ctx context.Context, d vectordata.CollectionDefinition, p vectordata.VectorProperty, top int, opts vectordata.ModelSearchOptions, source iter.Seq2[vectordata.ModelRecord, error], measure Measure) ([]vectordata.ModelSearchResult, error) {
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return nil, err
	}
	filter, err := freezeFilter(opts.Filter, d)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := top + opts.Skip
	best := candidates{}
	for r, err := range source {
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		match, err := vectordata.MatchModelFilter(filter, r, d)
		if err != nil {
			return nil, err
		}
		if !match || !vectordata.HasModelVector(r, p) {
			continue
		}
		distance, err := measure(r)
		if err != nil {
			return nil, err
		}
		score := vectordata.ScoreFromDistance(p.Metric, distance)
		if opts.DistanceThreshold != nil && distance > *opts.DistanceThreshold || opts.ScoreThreshold != nil && score < *opts.ScoreThreshold {
			continue
		}
		candidate := vectordata.ModelSearchResult{Record: r, Distance: distance, Score: score, ScoreKind: vectordata.ScoreSimilarity}
		if len(best) < limit {
			heap.Push(&best, candidate)
		} else if better(candidate, best[0]) {
			best[0] = candidate
			heap.Fix(&best, 0)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(best, func(i, j int) bool { return better(best[i], best[j]) })
	start := min(opts.Skip, len(best))
	out := make([]vectordata.ModelSearchResult, len(best)-start)
	for i, result := range best[start:] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result.Record = vectordata.CloneModelRecord(result.Record, d, opts.IncludeVectors)
		out[i] = result
	}
	return out, nil
}

func better(a, b vectordata.ModelSearchResult) bool {
	if a.Distance != b.Distance {
		return a.Distance < b.Distance
	}
	switch key := a.Record.Key.(type) {
	case string:
		return strings.Compare(key, b.Record.Key.(string)) < 0
	case int64:
		return cmp.Compare(key, b.Record.Key.(int64)) < 0
	default:
		return vectordata.CompareModelValues(a.Record.Key, b.Record.Key) < 0
	}
}

// A max-heap exposes the worst retained result, including the largest key in a tie.
type candidates []vectordata.ModelSearchResult

func (h candidates) Len() int           { return len(h) }
func (h candidates) Less(i, j int) bool { return better(h[j], h[i]) }
func (h candidates) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *candidates) Push(value any)    { *h = append(*h, value.(vectordata.ModelSearchResult)) }
func (h *candidates) Pop() any {
	last := len(*h) - 1
	value := (*h)[last]
	(*h)[last] = vectordata.ModelSearchResult{}
	*h = (*h)[:last]
	return value
}

// DenseDistance receives normalized, dimension-checked float32/float16 values.
func DenseDistance(a, b []float32, metric vectordata.DistanceMetric) float64 {
	if metric == vectordata.DistanceL1 {
		var sum float64
		for i, x := range a {
			sum += math.Abs(float64(x) - float64(b[i]))
		}
		return sum
	}
	var dot, aa, bb, sum float64
	for i, x := range a {
		xx, yy := float64(x), float64(b[i])
		dot += xx * yy
		aa += xx * xx
		bb += yy * yy
		delta := xx - yy
		sum += delta * delta
	}
	switch metric {
	case vectordata.DistanceL2:
		return math.Sqrt(sum)
	case vectordata.DistanceInnerProduct:
		return -dot
	default:
		return 1 - math.Max(-1, math.Min(1, dot/(math.Sqrt(aa)*math.Sqrt(bb))))
	}
}
