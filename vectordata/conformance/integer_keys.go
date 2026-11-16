package conformance

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type integerKey interface {
	~int | ~int8 | ~int16 | ~int32 | ~uint | ~uint8 | ~uint16 | ~uint32
}
type smallSignedID int16
type smallUnsignedID uint32
type integerKeyModel[K integerKey] struct {
	ID     K         `vectordata:"key"`
	Body   string    `vectordata:"data"`
	Vector []float32 `vectordata:"vector,dimensions=2,metric=l2"`
}

func runIntegerKeys(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	t.Run("int8", func(t *testing.T) { runIntegerKey[int8](t, factory, caps, []int8{math.MinInt8, 0, math.MaxInt8}) })
	t.Run("int16", func(t *testing.T) { runIntegerKey[int16](t, factory, caps, []int16{math.MinInt16, 0, math.MaxInt16}) })
	t.Run("int32", func(t *testing.T) { runIntegerKey[int32](t, factory, caps, []int32{math.MinInt32, 0, math.MaxInt32}) })
	t.Run("int", func(t *testing.T) { runIntegerKey[int](t, factory, caps, []int{-1, 0, 1}) })
	t.Run("uint8", func(t *testing.T) { runIntegerKey[uint8](t, factory, caps, []uint8{0, 1, math.MaxUint8}) })
	t.Run("uint16", func(t *testing.T) { runIntegerKey[uint16](t, factory, caps, []uint16{0, 1, math.MaxUint16}) })
	t.Run("uint32", func(t *testing.T) { runIntegerKey[uint32](t, factory, caps, []uint32{0, 1, math.MaxUint32}) })
	t.Run("uint", func(t *testing.T) { runIntegerKey[uint](t, factory, caps, []uint{0, 1, 2}) })
	t.Run("signed_alias", func(t *testing.T) {
		runIntegerKey[smallSignedID](t, factory, caps, []smallSignedID{math.MinInt16, 0, math.MaxInt16})
	})
	t.Run("unsigned_alias", func(t *testing.T) {
		runIntegerKey[smallUnsignedID](t, factory, caps, []smallUnsignedID{0, 1, math.MaxUint32})
	})
}

func runIntegerKey[K integerKey](t *testing.T, factory Factory, caps vectordata.ProviderCapabilities, keys []K) {
	codec, err := vectordata.NewStructCodec[integerKeyModel[K]](nil)
	if err != nil {
		t.Fatal(err)
	}
	d := codec.Definition()
	if !slices.Contains(caps.KeyTypes, d.Key.Type) {
		t.Skip("canonical key type not advertised")
	}
	ctx := context.Background()
	store := factory(t)
	c, err := vectordata.EnsureTypedModelCollection[K, integerKeyModel[K]](ctx, store, "integer_keys", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := make([]integerKeyModel[K], len(keys))
	for i, key := range keys {
		input[i] = integerKeyModel[K]{ID: key, Body: "original", Vector: []float32{float32(i), 1}}
	}
	gotKeys, err := c.Insert(ctx, input)
	if err != nil || !slices.Equal(gotKeys, keys) {
		t.Fatalf("insert keys: %v, %v", gotKeys, err)
	}
	for _, key := range keys {
		row, err := c.Get(ctx, key, vectordata.RetrievalOptions{})
		if err != nil || row.ID != key {
			t.Fatalf("get: %+v, %v", row, err)
		}
	}
	rows, err := c.GetMany(ctx, keys, vectordata.RetrievalOptions{})
	if err != nil || len(rows) != len(keys) {
		t.Fatalf("batch read: %+v, %v", rows, err)
	}
	seen := 0
	for _, err := range c.GetManyIter(ctx, keys, vectordata.RetrievalOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		seen++
	}
	if seen != len(keys) {
		t.Fatalf("iterator: %d rows", seen)
	}
	rows, err = c.Query(ctx, vectordata.Gte(vectordata.Column("ID"), keys[0]), len(keys), vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "ID"}}})
	if err != nil || len(rows) != len(keys) {
		t.Fatalf("ordered query: %+v, %v", rows, err)
	}
	for i, row := range rows {
		if row.ID != keys[i] {
			t.Fatalf("order: %+v", rows)
		}
	}
	hits, err := c.Search(ctx, []float32{0, 1}, len(keys), vectordata.ModelSearchOptions{})
	if err != nil || len(hits) != len(keys) {
		t.Fatalf("search: %+v, %v", hits, err)
	}
	for i, hit := range hits {
		if hit.Item.ID != keys[i] {
			t.Fatalf("search key: %+v", hit)
		}
	}
	input[0].Body = "updated"
	gotKeys, err = c.Upsert(ctx, input[:1])
	if err != nil || len(gotKeys) != 1 || gotKeys[0] != keys[0] {
		t.Fatalf("upsert: %v, %v", gotKeys, err)
	}
	row, err := c.Get(ctx, keys[0], vectordata.RetrievalOptions{})
	if err != nil || row.Body != "updated" {
		t.Fatalf("updated row: %+v, %v", row, err)
	}
	if count, err := c.Delete(ctx, keys); err != nil || count != int64(len(keys)) {
		t.Fatalf("delete: %d, %v", count, err)
	}
	if _, err := c.Get(ctx, keys[0], vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrNotFound) {
		t.Fatalf("deleted key: %v", err)
	}
}
