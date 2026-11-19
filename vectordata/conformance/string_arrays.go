package conformance

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type stringLabel string
type stringLabels []stringLabel

type stringArrayModel struct {
	ID      string        `vectordata:"key"`
	Tags    stringLabels  `vectordata:"data,name=tag_values,index"`
	Pointer *stringLabels `vectordata:"data"`
	Vector  []float32     `vectordata:"vector,dimensions=2,metric=l2"`
}

func runNamedStringArrays(t *testing.T, factory Factory) {
	ctx := t.Context()
	store := factory(t)
	c, err := vectordata.EnsureTypedModelCollection[string, stringArrayModel](ctx, store, "named_strings", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	empty, pointer := stringLabels{}, stringLabels{"pointer"}
	input := stringArrayModel{ID: "a", Tags: stringLabels{"go", "guide"}, Pointer: &pointer, Vector: []float32{1, 0}}
	if _, err := c.Insert(ctx, []stringArrayModel{input, {ID: "empty", Tags: empty, Pointer: &empty, Vector: []float32{0, 1}}, {ID: "nil", Vector: []float32{1, 1}}}); err != nil {
		t.Fatal(err)
	}
	input.Tags[0], pointer[0] = "changed", "changed"
	got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	want := stringArrayModel{ID: "a", Tags: stringLabels{"go", "guide"}, Pointer: &stringLabels{"pointer"}, Vector: []float32{1, 0}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("typed/copy round trip: %+v, %v", got, err)
	}
	got.Tags[0], (*got.Pointer)[0] = "changed", "changed"
	got, err = c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("returned values alias storage: %+v, %v", got, err)
	}
	for _, id := range []string{"empty", "nil"} {
		got, err := c.Get(ctx, id, vectordata.RetrievalOptions{})
		if err != nil || got.Tags == nil || len(got.Tags) != 0 {
			t.Fatalf("legacy typed nil/empty %s: %+v, %v", id, got, err)
		}
		if id == "empty" && (got.Pointer == nil || *got.Pointer == nil || len(*got.Pointer) != 0) || id == "nil" && got.Pointer != nil {
			t.Fatalf("pointer NULL/empty %s: %+v", id, got)
		}
	}
	field := vectordata.Column("Tags")
	for _, filter := range []vectordata.Filter{vectordata.Eq(field, stringLabels{"go", "guide"}), vectordata.In(field, stringLabels{"go", "guide"}), vectordata.Contains(field, "go"), vectordata.ContainsAll(field, "go", "guide")} {
		rows, err := c.Query(ctx, filter, 5, vectordata.QueryOptions{})
		if err != nil || len(rows) != 1 || rows[0].ID != "a" {
			t.Fatalf("typed string-array query: %+v, %v", rows, err)
		}
		if count, err := c.Count(ctx, filter); err != nil || count != 1 {
			t.Fatalf("typed string-array count: %d, %v", count, err)
		}
		hits, err := c.Search(ctx, []float32{1, 0}, 3, vectordata.ModelSearchOptions{Filter: filter})
		if err != nil || len(hits) != 1 || hits[0].Item.ID != "a" {
			t.Fatalf("typed string-array search: %+v, %v", hits, err)
		}
	}
	base, err := store.GetModelCollection("named_strings", c.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Upsert(ctx, []vectordata.ModelRecord{
		{Key: "new", Data: map[string]any{"Tags": stringLabels{"valid"}}, Vectors: map[string][]float32{"Vector": {0, 1}}},
		{Key: "a", Data: map[string]any{"Tags": []any{"wrong"}}, Vectors: map[string][]float32{"Vector": {1, 0}}},
	}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("invalid string array batch: %v", err)
	}
	if count, err := c.Count(ctx, nil); err != nil || count != 3 {
		t.Fatalf("partial invalid write: %d, %v", count, err)
	}
	// Reopen with an ordinary []string mapping: registration and canonical
	// storage must not change merely because Go uses named representations.
	type plain struct {
		ID      string
		Tags    []string
		Pointer *[]string
		Vector  []float32
	}
	definition := c.Definition()
	reopened, err := vectordata.GetTypedModelCollection[string, plain](store, c.Name(), &definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	plainRow, err := reopened.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || !reflect.DeepEqual(plainRow.Tags, []string{"go", "guide"}) {
		t.Fatalf("canonical reopen: %+v, %v", plainRow, err)
	}
}
