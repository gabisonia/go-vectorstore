package conformance

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type unsignedID uint64
type unsignedModel struct {
	ID     unsignedID `vectordata:"key,name=record_id"`
	Body   string     `vectordata:"data,fulltext"`
	Vector []float32  `vectordata:"vector,dimensions=2,metric=l2"`
}

func runNativeKeys(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	t.Run("integer_widths", func(t *testing.T) { runIntegerKeys(t, factory, caps) })
	if slices.Contains(caps.KeyTypes, vectordata.KeyUint64) {
		t.Run("uint64", func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			c, err := vectordata.EnsureTypedModelCollection[unsignedID, unsignedModel](ctx, store, "unsigned_keys", nil, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := []uint64{0, 1, 9007199254740992, 9007199254740993, math.MaxInt64, uint64(math.MaxInt64) + 1, math.MaxUint64 - 1, math.MaxUint64}
			inputs := make([]unsignedModel, len(want))
			for i, key := range want {
				inputs[i] = unsignedModel{ID: unsignedID(key), Body: "unsigned key", Vector: []float32{1, 0}}
			}
			keys, err := c.Insert(ctx, inputs)
			if err != nil || len(keys) != len(want) {
				t.Fatalf("insert: %v, %v", keys, err)
			}
			for i, key := range want {
				if keys[i] != unsignedID(key) {
					t.Fatalf("returned key %d: %v", i, keys[i])
				}
				got, err := c.Get(ctx, unsignedID(key), vectordata.RetrievalOptions{})
				if err != nil || got.ID != unsignedID(key) {
					t.Fatalf("get %d: %+v, %v", key, got, err)
				}
				filter := vectordata.Eq(vectordata.Column("ID"), unsignedID(key))
				if n, err := c.Count(ctx, filter); err != nil || n != 1 {
					t.Fatalf("exact equality %d: %d, %v", key, n, err)
				}
				gotMany, err := c.GetMany(ctx, []unsignedID{unsignedID(key)}, vectordata.RetrievalOptions{})
				if err != nil || len(gotMany) != 1 || gotMany[0].ID != unsignedID(key) {
					t.Fatalf("get-many %d: %v, %v", key, gotMany, err)
				}
			}
			rows, err := c.Query(ctx, vectordata.Gt(vectordata.Column("ID"), uint64(math.MaxInt64)), 8, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "ID"}}})
			if err != nil || len(rows) != 3 || rows[0].ID != unsignedID(uint64(math.MaxInt64)+1) || rows[2].ID != math.MaxUint64 {
				t.Fatalf("unsigned ordering: %v, %v", rows, err)
			}
			hits, err := c.Search(ctx, []float32{1, 0}, len(want), vectordata.ModelSearchOptions{})
			if err != nil || len(hits) != len(want) {
				t.Fatalf("search: %v, %v", hits, err)
			}
			foundKeys := make([]uint64, len(hits))
			for i, hit := range hits {
				foundKeys[i] = uint64(hit.Item.ID)
			}
			slices.Sort(foundKeys)
			if !slices.Equal(foundKeys, want) {
				t.Fatalf("search key precision: %v", foundKeys)
			}
			for _, filter := range []vectordata.Filter{
				vectordata.In(vectordata.Column("ID"), uint64(math.MaxUint64), uint64(math.MaxUint64-1)),
				vectordata.Gte(vectordata.Column("ID"), uint64(math.MaxUint64-1)),
				vectordata.And(vectordata.Gt(vectordata.Column("ID"), uint64(9007199254740992)), vectordata.Lt(vectordata.Column("ID"), uint64(math.MaxInt64))),
			} {
				count, err := c.Count(ctx, filter)
				wantCount := int64(2)
				if _, ok := filter.(vectordata.AndFilter); ok {
					wantCount = 1
				}
				if err != nil || count != wantCount {
					t.Fatalf("boundary filter: %d, %v", count, err)
				}
			}
			if caps.HybridSearch {
				hits, err := c.HybridSearch(ctx, []float32{1, 0}, "unsigned", 2, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{Filter: vectordata.Gte(vectordata.Column("ID"), uint64(math.MaxUint64-1))}})
				if err != nil || len(hits) != 2 || hits[0].Item.ID != math.MaxUint64-1 || hits[1].Item.ID != math.MaxUint64 {
					t.Fatalf("hybrid uint64 keys: %v, %v", hits, err)
				}
			}
			base, err := store.GetModelCollection(c.Name(), c.Definition(), vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, invalid := range []any{int64(-1), "18446744073709551615", float64(1), nil} {
				if _, err := base.Get(ctx, invalid, vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrInvalidRecord) {
					t.Fatalf("invalid unsigned key %v: %v", invalid, err)
				}
			}
			inputs[len(inputs)-1].Body = "updated"
			if _, err := c.Upsert(ctx, inputs[len(inputs)-1:]); err != nil {
				t.Fatal(err)
			}
			if got, err := c.Get(ctx, math.MaxUint64, vectordata.RetrievalOptions{}); err != nil || got.Body != "updated" {
				t.Fatalf("unsigned update: %v, %v", got, err)
			}
			if n, err := c.Delete(ctx, []unsignedID{math.MaxUint64, math.MaxUint64 - 1}); err != nil || n != 2 {
				t.Fatalf("unsigned delete: %d, %v", n, err)
			}
			if _, err := c.Get(ctx, math.MaxUint64, vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrNotFound) {
				t.Fatalf("unsigned deletion: %v", err)
			}
			if _, err := c.Insert(ctx, inputs[len(inputs)-2:]); err != nil {
				t.Fatal(err)
			}
			if caps.MigrationPlanning {
				d := c.Definition()
				plan, err := store.(vectordata.MigrationPlanner).PlanMigration(ctx, c.Name(), d)
				if err != nil || plan.HasManualWork() || len(plan.Steps) != 0 || plan.Finalization != nil {
					t.Fatalf("uint64 migration: %+v, %v", plan, err)
				}
				if caps.MigrationApplication {
					d.Data = append(d.Data, vectordata.DataProperty{Name: "Optional", Type: vectordata.TypeString, Optional: true})
					plan, err = store.(vectordata.MigrationPlanner).PlanMigration(ctx, c.Name(), d)
					if err != nil {
						t.Fatal(err)
					}
					if err := store.(vectordata.MigrationApplier).ApplyMigration(ctx, plan); err != nil {
						t.Fatal(err)
					}
					base, err := store.EnsureModelCollection(ctx, c.Name(), d, vectordata.ModelCollectionOptions{})
					if err != nil {
						t.Fatal(err)
					}
					if got, err := base.Get(ctx, uint64(math.MaxUint64), vectordata.RetrievalOptions{}); err != nil || got.Key != uint64(math.MaxUint64) {
						t.Fatalf("migrated unsigned key: %v, %v", got, err)
					}
				}
			}
		})
	}
	if slices.Contains(caps.GeneratedKeyTypes, vectordata.KeyUUID) {
		t.Run("uuid_bindings", func(t *testing.T) { runUUIDBindings(t, factory) })
		t.Run("generated_uuid", func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			type model struct {
				ID   nativeUUID `vectordata:"key,type=uuid,generated"`
				Body string     `vectordata:"data"`
			}
			c, err := vectordata.EnsureTypedModelCollection[nativeUUID, model](ctx, store, "uuid_keys", nil, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			inputs := []model{{Body: "one"}, {Body: "two"}, {ID: nativeUUID{Text: nativeUUIDA}, Body: "explicit"}}
			keys, err := c.Upsert(ctx, inputs)
			if err != nil || len(keys) != 3 {
				t.Fatalf("UUID upsert: %v, %v", keys, err)
			}
			if !reflect.DeepEqual(inputs, []model{{Body: "one"}, {Body: "two"}, {ID: nativeUUID{Text: nativeUUIDA}, Body: "explicit"}}) {
				t.Fatal("generated UUID mutated input models")
			}
			if keys[0] == keys[1] || keys[0].Text == nativeUUIDA || keys[2].Text != nativeUUIDA {
				t.Fatalf("UUID uniqueness/preservation: %v", keys)
			}
			for i, key := range keys {
				normalized, err := c.Definition().NormalizeKey(key)
				if err != nil || normalized != key.Text {
					t.Fatalf("UUID format: %v, %v", key, err)
				}
				if i < 2 && (key.Text[14] != '4' || !strings.ContainsRune("89ab", rune(key.Text[19]))) {
					t.Fatalf("expected UUID v4: %v", key)
				}
				got, err := c.Get(ctx, key, vectordata.RetrievalOptions{})
				if err != nil || got.ID != key || got.Body != inputs[i].Body {
					t.Fatalf("generated UUID get: %v, %v", got, err)
				}
			}
			inputs[0].ID, inputs[0].Body = keys[0], "updated"
			if gotKeys, err := c.Upsert(ctx, inputs[:1]); err != nil || gotKeys[0] != keys[0] {
				t.Fatalf("UUID update: %v, %v", gotKeys, err)
			}
			if count, err := c.Count(ctx, nil); err != nil || count != 3 {
				t.Fatalf("UUID update inserted another row: %d, %v", count, err)
			}
			base, err := store.GetModelCollection(c.Name(), c.Definition(), vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := base.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Body": "pending"}}, {Key: nativeUUIDA, Data: map[string]any{"Body": "duplicate"}}}); err == nil {
				t.Fatal("duplicate batch succeeded")
			}
			if count, err := c.Count(ctx, nil); err != nil || count != 3 {
				t.Fatalf("partial generated UUID batch: %d, %v", count, err)
			}
			if caps.MigrationPlanning {
				plan, err := store.(vectordata.MigrationPlanner).PlanMigration(ctx, c.Name(), c.Definition())
				if err != nil || plan.HasManualWork() || len(plan.Steps) != 0 || plan.Finalization != nil {
					t.Fatalf("generated UUID plan: %+v, %v", plan, err)
				}
			}
			if n, err := c.Delete(ctx, keys); err != nil || n != 3 {
				t.Fatalf("generated UUID delete: %d, %v", n, err)
			}
		})
	}
}
