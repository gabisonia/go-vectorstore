package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type singleItem struct {
	ID     string    `vectordata:"key"`
	Body   string    `vectordata:"data"`
	Vector []float32 `vectordata:"vector,dimensions=2"`
}
type generatedSingleItem struct {
	ID   int64  `vectordata:"key,generated"`
	Body string `vectordata:"data"`
}

func runSingleRecordOperations(t *testing.T, factory Factory) {
	ctx := context.Background()
	store := factory(t)
	c, err := vectordata.EnsureTypedModelCollection[string, singleItem](ctx, store, "single_records", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := singleItem{ID: "one", Body: "first", Vector: []float32{1, 0}}
	if key, err := c.InsertOne(ctx, input); err != nil || key != "one" {
		t.Fatalf("insert one: %v %v", key, err)
	}
	input.Vector[0] = 100
	if _, err := c.InsertOne(ctx, input); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("duplicate: %v", err)
	}
	row, found, err := c.TryGet(ctx, "one", vectordata.RetrievalOptions{})
	if err != nil || !found || row.Body != "first" || row.Vector != nil {
		t.Fatalf("try get: %+v %v %v", row, found, err)
	}
	row, found, err = c.TryGet(ctx, "one", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || !found || len(row.Vector) != 2 || row.Vector[0] != 1 {
		t.Fatalf("projected vector: %+v %v %v", row, found, err)
	}
	row.Vector[0] = 100
	if key, err := c.UpsertOne(ctx, singleItem{ID: "one", Body: "replaced", Vector: []float32{0, 1}}); err != nil || key != "one" {
		t.Fatalf("upsert one: %v %v", key, err)
	}
	base, err := store.GetModelCollection(c.Name(), c.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dynamic, found, err := vectordata.TryGet(ctx, base, "one", vectordata.RetrievalOptions{})
	if err != nil || !found || dynamic.Data["Body"] != "replaced" || dynamic.Vectors != nil {
		t.Fatalf("dynamic get: %+v %v %v", dynamic, found, err)
	}
	record := vectordata.ModelRecord{Key: "two", Data: map[string]any{"Body": "dynamic"}, Vectors: map[string][]float32{"Vector": {1, 0}}}
	if key, err := vectordata.UpsertOne(ctx, base, record); err != nil || key != "two" {
		t.Fatalf("dynamic upsert: %v %v", key, err)
	}
	record.Key = "three"
	if key, err := vectordata.InsertOne(ctx, base, record); err != nil || key != "three" {
		t.Fatalf("dynamic insert: %v %v", key, err)
	}
	if _, found, err := c.TryGet(ctx, "absent", vectordata.RetrievalOptions{}); err != nil || found {
		t.Fatalf("typed absence: %v %v", found, err)
	}
	if _, found, err := vectordata.TryGet(ctx, base, "absent", vectordata.RetrievalOptions{}); err != nil || found {
		t.Fatalf("dynamic absence: %v %v", found, err)
	}
	if _, err := c.Get(ctx, "absent", vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrNotFound) {
		t.Fatalf("Get contract changed: %v", err)
	}
	if deleted, err := c.DeleteOne(ctx, "absent"); err != nil || deleted {
		t.Fatalf("missing delete: %v %v", deleted, err)
	}
	if deleted, err := c.DeleteOne(ctx, "one"); err != nil || !deleted {
		t.Fatalf("delete one: %v %v", deleted, err)
	}
	if deleted, err := vectordata.DeleteOne(ctx, base, "two"); err != nil || !deleted {
		t.Fatalf("dynamic delete: %v %v", deleted, err)
	}
	if deleted, err := vectordata.DeleteOne(ctx, base, "two"); err != nil || deleted {
		t.Fatalf("repeated delete: %v %v", deleted, err)
	}
	if _, found, err := c.TryGet(ctx, "one", vectordata.RetrievalOptions{}); err != nil || found {
		t.Fatalf("deleted read: %v %v", found, err)
	}
	if err := c.EnsureDeleted(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := c.TryGet(ctx, "one", vectordata.RetrievalOptions{}); err == nil || found {
		t.Fatalf("missing collection suppressed: %v %v", found, err)
	}
	generated, err := vectordata.EnsureTypedModelCollection[int64, *generatedSingleItem](ctx, store, "single_generated", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	value := &generatedSingleItem{Body: "generated"}
	key, err := generated.UpsertOne(ctx, value)
	if err != nil || key <= 0 || value.ID != 0 {
		t.Fatalf("generated key/ownership: %d %+v %v", key, value, err)
	}
	saved, found, err := generated.TryGet(ctx, key, vectordata.RetrievalOptions{})
	if err != nil || !found || saved == nil || saved.ID != key || saved.Body != "generated" {
		t.Fatalf("generated read: %+v %v %v", saved, found, err)
	}
}
