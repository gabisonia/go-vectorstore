package vectordata_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type integerWriteSpy struct {
	vectordata.ModelCollection
	calls int
}

func (c *integerWriteSpy) Insert(ctx context.Context, records []vectordata.ModelRecord) ([]any, error) {
	c.calls++
	return c.ModelCollection.Insert(ctx, records)
}
func (c *integerWriteSpy) Upsert(ctx context.Context, records []vectordata.ModelRecord) ([]any, error) {
	c.calls++
	return c.ModelCollection.Upsert(ctx, records)
}

type recordPassthroughCodec struct{}

func (recordPassthroughCodec) Encode(r vectordata.ModelRecord) (vectordata.ModelRecord, error) {
	return r, nil
}
func (recordPassthroughCodec) Decode(r vectordata.ModelRecord) (vectordata.ModelRecord, error) {
	return r, nil
}

func TestTypedIntegerKeysRejectOverflowBeforeProviderWrites(t *testing.T) {
	t.Run("signed", func(t *testing.T) {
		testIntegerWriteOverflow[int16](t, vectordata.KeyInt64, []any{int64(math.MaxInt16) + 1, int64(math.MinInt16) - 1})
	})
	t.Run("unsigned", func(t *testing.T) {
		testIntegerWriteOverflow[uint8](t, vectordata.KeyUint64, []any{uint64(math.MaxUint8) + 1, int64(-1)})
	})
}

func testIntegerWriteOverflow[K comparable](t *testing.T, kind vectordata.KeyType, invalid []any) {
	ctx := context.Background()
	base, err := memory.New().EnsureModelCollection(ctx, "keys", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: kind}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Insert(ctx, []vectordata.ModelRecord{{Key: 1, Data: map[string]any{"Body": "original"}}}); err != nil {
		t.Fatal(err)
	}
	spy := &integerWriteSpy{ModelCollection: base}
	c, err := vectordata.NewTypedModelCollection[K, vectordata.ModelRecord](spy, recordPassthroughCodec{})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range invalid {
		for _, write := range []func(context.Context, []vectordata.ModelRecord) ([]K, error){c.Insert, c.Upsert} {
			// A later invalid key must reject the entire batch, including an
			// earlier valid update and an earlier valid insertion.
			rows := []vectordata.ModelRecord{{Key: 1, Data: map[string]any{"Body": "changed"}}, {Key: 2, Data: map[string]any{"Body": "new"}}, {Key: bad, Data: map[string]any{"Body": "invalid"}}}
			keys, err := write(ctx, rows)
			if !errors.Is(err, vectordata.ErrInvalidRecord) || keys != nil || spy.calls != 0 {
				t.Fatalf("overflow reached provider: keys=%v calls=%d err=%v", keys, spy.calls, err)
			}
			count, err := base.Count(ctx, nil)
			if err != nil || count != 1 {
				t.Fatalf("partial batch: %d, %v", count, err)
			}
			row, err := base.Get(ctx, 1, vectordata.RetrievalOptions{})
			if err != nil || row.Data["Body"] != "original" {
				t.Fatalf("partial update: %+v, %v", row, err)
			}
		}
	}
}

func TestGeneratedKeysRequireFullWidthTypedKey(t *testing.T) {
	type model struct {
		ID int64 `vectordata:"key,generated"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	base, err := store.GetModelCollection("generated", codec.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vectordata.NewTypedModelCollection[int32, model](base, codec); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("narrow generated key: %v", err)
	}
	if exists, err := base.Exists(context.Background()); err != nil || exists {
		t.Fatalf("invalid wrapper created storage: %t, %v", exists, err)
	}
	if _, err := vectordata.NewTypedModelCollection[int64, model](base, codec); err != nil {
		t.Fatal(err)
	}
	if _, err := vectordata.NewTypedModelCollection[uint16, model](base, codec); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("unsigned key accepted signed schema: %v", err)
	}
}
