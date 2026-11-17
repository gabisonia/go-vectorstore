package vectordata

import (
	"context"
	"errors"
	"fmt"
)

// InsertOne inserts a single model record and returns its key. It uses one batch
// operation, preserving provider validation, generated keys and atomicity.
func InsertOne(ctx context.Context, c ModelCollection, record ModelRecord) (any, error) {
	if err := singleRecordReady(ctx, c); err != nil {
		return nil, err
	}
	keys, err := c.Insert(ctx, []ModelRecord{record})
	return singleRecordKey(keys, err)
}

// UpsertOne inserts or replaces a single model record and returns its key.
// Like Upsert, it does not create a missing collection or mutate caller records.
func UpsertOne(ctx context.Context, c ModelCollection, record ModelRecord) (any, error) {
	if err := singleRecordReady(ctx, c); err != nil {
		return nil, err
	}
	keys, err := c.Upsert(ctx, []ModelRecord{record})
	return singleRecordKey(keys, err)
}

// TryGet returns a record and found=true, or a zero record, false, nil when Get
// reports ErrNotFound. All other provider errors are preserved, including missing
// collections and cancellation. Retrieval projection follows Get.
func TryGet(ctx context.Context, c ModelCollection, key any, opts RetrievalOptions) (ModelRecord, bool, error) {
	if err := singleRecordReady(ctx, c); err != nil {
		return ModelRecord{}, false, err
	}
	record, err := c.Get(ctx, key, opts)
	if errors.Is(err, ErrNotFound) {
		return ModelRecord{}, false, nil
	}
	if err != nil {
		return ModelRecord{}, false, err
	}
	return record, true, nil
}

// DeleteOne deletes one key and reports whether a record was deleted. A missing
// record returns false, nil. Missing collection behavior follows the provider.
func DeleteOne(ctx context.Context, c ModelCollection, key any) (bool, error) {
	if err := singleRecordReady(ctx, c); err != nil {
		return false, err
	}
	count, err := c.Delete(ctx, []any{key})
	return singleRecordDeleted(count, err)
}

func singleRecordReady(ctx context.Context, collection any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if isNilValue(collection) {
		return fmt.Errorf("%w: collection is required", ErrSchemaMismatch)
	}
	return nil
}

func singleRecordKey[K any](keys []K, err error) (key K, resultErr error) {
	if err != nil {
		return key, err
	}
	if len(keys) != 1 {
		return key, fmt.Errorf("%w: single-record write returned %d keys, expected 1", ErrInvalidRecord, len(keys))
	}
	if isNilValue(keys[0]) {
		return key, fmt.Errorf("%w: single-record write returned a nil key", ErrInvalidRecord)
	}
	return keys[0], nil
}

func singleRecordDeleted(count int64, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	if count < 0 || count > 1 {
		return false, fmt.Errorf("%w: single-record delete returned count %d, expected 0 or 1", ErrInvalidRecord, count)
	}
	return count == 1, nil
}

func (c *TypedModelCollection[K, T]) singleRecordReady(ctx context.Context) error {
	if err := singleRecordReady(ctx, c); err != nil {
		return err
	}
	if isNilValue(c.base) || isNilValue(c.codec) {
		return fmt.Errorf("%w: collection and codec are required", ErrSchemaMismatch)
	}
	return nil
}

// InsertOne inserts one typed model, retaining Insert's key and codec validation.
func (c *TypedModelCollection[K, T]) InsertOne(ctx context.Context, value T) (K, error) {
	if err := c.singleRecordReady(ctx); err != nil {
		var zero K
		return zero, err
	}
	keys, err := c.Insert(ctx, []T{value})
	return singleRecordKey(keys, err)
}

// UpsertOne inserts or replaces one typed model. Generated keys are returned;
// the supplied value is not modified, including pointer models.
func (c *TypedModelCollection[K, T]) UpsertOne(ctx context.Context, value T) (K, error) {
	if err := c.singleRecordReady(ctx); err != nil {
		var zero K
		return zero, err
	}
	keys, err := c.Upsert(ctx, []T{value})
	return singleRecordKey(keys, err)
}

// TryGet is Get with an explicit found result for missing records. Codec errors
// are preserved even if they match ErrNotFound; only provider absence is mapped.
// On any error, the returned value is zero and found is false.
func (c *TypedModelCollection[K, T]) TryGet(ctx context.Context, key K, opts RetrievalOptions) (T, bool, error) {
	var zero T
	if err := c.singleRecordReady(ctx); err != nil {
		return zero, false, err
	}
	record, found, err := TryGet(ctx, c.base, key, opts)
	if err != nil || !found {
		return zero, false, err
	}
	value, err := c.codec.Decode(record)
	if err != nil {
		return zero, false, err
	}
	return value, true, nil
}

// DeleteOne deletes one typed key. Missing records return false, nil.
func (c *TypedModelCollection[K, T]) DeleteOne(ctx context.Context, key K) (bool, error) {
	if err := c.singleRecordReady(ctx); err != nil {
		return false, err
	}
	count, err := c.Delete(ctx, []K{key})
	return singleRecordDeleted(count, err)
}
