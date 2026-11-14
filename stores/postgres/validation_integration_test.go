//go:build integration

package postgres

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestIntegrationInvalidVectorsRollBackAndReturnTypedErrors(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "docs", Dimension: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, vector := range [][]float32{{0, 0}, {1, float32(math.NaN())}, {1, float32(math.Inf(1))}} {
		records := benchmarkRecords(maxRowsPerStatement+1, 2)
		records[len(records)-1].Vector = vector
		for _, write := range []func(context.Context, []vectordata.Record) error{collection.Insert, collection.Upsert} {
			if err := write(ctx, records); !errors.Is(err, vectordata.ErrInvalidVector) || !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("expected invalid vector/record, got %v", err)
			}
			count, err := collection.Count(ctx, nil)
			if err != nil || count != 0 {
				t.Fatalf("invalid vector left partial writes: %d, %v", count, err)
			}
		}
		if _, err := collection.SearchByVector(ctx, vector, 1, vectordata.SearchOptions{}); !errors.Is(err, vectordata.ErrInvalidVector) {
			t.Fatalf("expected ErrInvalidVector, got %v", err)
		}
	}
}

func TestIntegrationMetadataKeysAreLiteral(t *testing.T) {
	pool := integrationPool(t)
	store := newTestStore(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: "docs", Dimension: 2})
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{
		"price": 1, " price ": 2, "": 3, "   ": 4, "owner's": 5,
		"nested": map[string]any{" key ": 6}, "array": []any{7},
	}
	if err := collection.Insert(ctx, []vectordata.Record{{ID: "keys", Vector: []float32{1, 0}, Metadata: metadata}}); err != nil {
		t.Fatal(err)
	}
	if err := collection.EnsureIndexes(ctx, vectordata.IndexOptions{Metadata: &vectordata.MetadataIndexOptions{UsePathOps: true}}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path  []string
		value int
	}{
		{[]string{"price"}, 1}, {[]string{" price "}, 2}, {[]string{""}, 3}, {[]string{"   "}, 4}, {[]string{"owner's"}, 5},
		{[]string{"nested", " key "}, 6}, {[]string{"array", "0"}, 7}, {[]string{"array", " 0"}, 7},
	} {
		for _, filter := range []vectordata.Filter{vectordata.Eq(vectordata.Metadata(tt.path...), tt.value), vectordata.In(vectordata.Metadata(tt.path...), tt.value), vectordata.Exists(vectordata.Metadata(tt.path...))} {
			count, err := collection.Count(ctx, filter)
			if err != nil || count != 1 {
				t.Fatalf("path %q: count %d, error %v", tt.path, count, err)
			}
			results, err := collection.SearchByVector(ctx, []float32{1, 0}, 1, vectordata.SearchOptions{Filter: filter})
			if err != nil || len(results) != 1 {
				t.Fatalf("path %q: %d results, error %v", tt.path, len(results), err)
			}
		}
	}
	if count, err := collection.Count(ctx, vectordata.Eq(vectordata.Metadata(" price "), 1)); err != nil || count != 0 {
		t.Fatalf("whitespace key confused with trimmed key: %d, %v", count, err)
	}
}
