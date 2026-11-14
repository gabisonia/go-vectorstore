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

func (c *TypedCollection[T]) Name() string           { return c.base.Name() }
func (c *TypedCollection[T]) Dimension() int         { return c.base.Dimension() }
func (c *TypedCollection[T]) Metric() DistanceMetric { return c.base.Metric() }
func (c *TypedCollection[T]) Delete(ctx context.Context, ids []string) (int64, error) {
	return c.base.Delete(ctx, ids)
}
func (c *TypedCollection[T]) Count(ctx context.Context, filter Filter) (int64, error) {
	return c.base.Count(ctx, filter)
}
func (c *TypedCollection[T]) EnsureIndexes(ctx context.Context, opts IndexOptions) error {
	return c.base.EnsureIndexes(ctx, opts)
}

func (c *TypedCollection[T]) Exists(ctx context.Context) (bool, error) {
	l, ok := c.base.(CollectionLifecycle)
	if !ok {
		return false, ErrUnsupported
	}
	return l.Exists(ctx)
}
func (c *TypedCollection[T]) EnsureExists(ctx context.Context) error {
	l, ok := c.base.(CollectionLifecycle)
	if !ok {
		return ErrUnsupported
	}
	return l.EnsureExists(ctx)
}
func (c *TypedCollection[T]) EnsureDeleted(ctx context.Context) error {
	l, ok := c.base.(CollectionLifecycle)
	if !ok {
		return ErrUnsupported
	}
	return l.EnsureDeleted(ctx)
}
func (c *TypedCollection[T]) GetWithOptions(ctx context.Context, id string, opts RetrievalOptions) (T, error) {
	q, ok := c.base.(RecordQueryer)
	if !ok {
		var zero T
		return zero, ErrUnsupported
	}
	r, err := q.GetWithOptions(ctx, id, opts)
	if err != nil {
		var zero T
		return zero, err
	}
	return c.codec.Decode(r)
}
func (c *TypedCollection[T]) GetMany(ctx context.Context, ids []string, opts RetrievalOptions) ([]T, error) {
	q, ok := c.base.(RecordQueryer)
	if !ok {
		return nil, ErrUnsupported
	}
	records, err := q.GetMany(ctx, ids, opts)
	if err != nil {
		return nil, err
	}
	return c.decodeRecords(records)
}
func (c *TypedCollection[T]) Query(ctx context.Context, filter Filter, top int, opts QueryOptions) ([]T, error) {
	q, ok := c.base.(RecordQueryer)
	if !ok {
		return nil, ErrUnsupported
	}
	records, err := q.Query(ctx, filter, top, opts)
	if err != nil {
		return nil, err
	}
	return c.decodeRecords(records)
}
func (c *TypedCollection[T]) decodeRecords(records []Record) ([]T, error) {
	out := make([]T, len(records))
	for i, r := range records {
		v, err := c.codec.Decode(r)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}
