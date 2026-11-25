package qdrant

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type nativeSearcher[K NativeKey, T any] struct {
	points        *NativePoints[K]
	defaultVector string
	decode        func(NamedPoint[K]) (T, error)
}

// NewNativeVectorSearcher adapts an attached collection for typed retrieval.
// Vector and filter names refer to physical fields. The decoder receives the
// ID and full payload from the ranked query, plus all vectors if requested.
// Equal scores retain Qdrant ordering. Thresholds and provider options are unsupported.
func NewNativeVectorSearcher[K NativeKey, T any](points *NativePoints[K], defaultVector string, decode func(NamedPoint[K]) (T, error)) (vectordata.VectorSearcher[T], error) {
	if points == nil || points.dimensions[defaultVector] == 0 || decode == nil {
		return nil, fmt.Errorf("%w: attached points, a configured default vector and a decoder are required", vectordata.ErrSchemaMismatch)
	}
	return &nativeSearcher[K, T]{points, defaultVector, decode}, nil
}

func (s *nativeSearcher[K, T]) SearchIter(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[T], error] {
	return func(yield func(vectordata.TypedModelSearchResult[T], error) bool) {
		var zero vectordata.TypedModelSearchResult[T]
		if err := ctx.Err(); err != nil {
			yield(zero, err)
			return
		}
		query, err := s.query(vector, top, opts)
		if err != nil {
			yield(zero, err)
			return
		}
		var out response[struct {
			Points []struct {
				NamedPoint[K]
				Score *float64
			}
		}]
		if _, err := s.points.store.request(ctx, http.MethodPost, s.points.path+"/points/query", query, &out); err != nil {
			yield(zero, err)
			return
		}
		for _, hit := range out.Result.Points {
			if err := ctx.Err(); err != nil {
				yield(zero, err)
				return
			}
			if hit.Score == nil {
				yield(zero, ErrCorrupt)
				return
			}
			item, err := s.decode(hit.NamedPoint)
			if err = errors.Join(err, ctx.Err()); err != nil {
				yield(zero, err)
				return
			}
			if !yield(vectordata.TypedModelSearchResult[T]{Item: item, Score: *hit.Score, Distance: 1 - *hit.Score, ScoreKind: vectordata.ScoreSimilarity}, nil) {
				return
			}
		}
	}
}

func (s *nativeSearcher[K, T]) query(vector []float32, top int, opts vectordata.ModelSearchOptions) (map[string]any, error) {
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return nil, err
	}
	if opts.ProviderOptions != nil || opts.DistanceThreshold != nil || opts.ScoreThreshold != nil {
		return nil, fmt.Errorf("%w: native typed search does not support thresholds or provider options", vectordata.ErrUnsupported)
	}
	native := NativeSearchOptions{Skip: opts.Skip}
	if opts.Filter != nil {
		eq, ok := opts.Filter.(vectordata.EqFilter)
		value, stringValue := eq.Value.(string)
		if !ok || !stringValue || eq.Field.Kind != vectordata.FieldColumn || len(eq.Field.Path) != 0 || !s.points.keywordFields[eq.Field.Name] {
			return nil, fmt.Errorf("%w: native typed search requires indexed keyword equality", vectordata.ErrUnsupported)
		}
		native.Keyword = &NativeKeywordFilter{Field: eq.Field.Name, Value: value}
	}
	name := opts.VectorProperty
	if name == "" {
		name = s.defaultVector
	}
	query, err := s.points.searchQuery(name, vector, top, native)
	if err != nil {
		return nil, err
	}
	query["with_payload"] = true
	query["with_vector"] = opts.IncludeVectors
	return query, nil
}
