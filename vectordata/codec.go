package vectordata

import "context"

// Codec maps between an application type and the Record model.
type Codec[T any] interface {
	Encode(value T) (Record, error)
	Decode(record Record) (T, error)
}

// TypedSearchResult wraps a typed item with ranking metrics.
type TypedSearchResult[T any] struct {
	Item     T
	Distance float64
	Score    float64
}

// TypedCollection adds type-safe helpers over a Record-based Collection.
type TypedCollection[T any] struct {
	base  Collection
	codec Codec[T]
}

// NewTypedCollection wraps a record collection with a codec.
func NewTypedCollection[T any](base Collection, codec Codec[T]) *TypedCollection[T] {
	return &TypedCollection[T]{base: base, codec: codec}
}

func (c *TypedCollection[T]) Insert(ctx context.Context, values []T) error {
	records, err := c.encodeMany(values)
	if err != nil {
		return err
	}
	return c.base.Insert(ctx, records)
}

func (c *TypedCollection[T]) Upsert(ctx context.Context, values []T) error {
	records, err := c.encodeMany(values)
	if err != nil {
		return err
	}
	return c.base.Upsert(ctx, records)
}

func (c *TypedCollection[T]) Get(ctx context.Context, id string) (T, error) {
	record, err := c.base.Get(ctx, id)
	if err != nil {
		var zero T
		return zero, err
	}
	return c.codec.Decode(record)
}

func (c *TypedCollection[T]) SearchByVector(ctx context.Context, vector []float32, topK int, opts SearchOptions) ([]TypedSearchResult[T], error) {
	results, err := c.base.SearchByVector(ctx, vector, topK, opts)
	if err != nil {
		return nil, err
	}
	out := make([]TypedSearchResult[T], 0, len(results))
	for _, result := range results {
		decoded, err := c.codec.Decode(result.Record)
		if err != nil {
			return nil, err
		}
		out = append(out, TypedSearchResult[T]{
			Item:     decoded,
			Distance: result.Distance,
			Score:    result.Score,
		})
	}
	return out, nil
}

func (c *TypedCollection[T]) encodeMany(values []T) ([]Record, error) {
	records := make([]Record, 0, len(values))
	for _, value := range values {
		record, err := c.codec.Encode(value)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}
