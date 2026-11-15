package conformance

import (
	"context"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"slices"
	"testing"
)

func runArraySets(t *testing.T, factory Factory) {
	ctx := context.Background()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Tags", Type: vectordata.TypeStringArray, Optional: true, IsIndexed: true}, {Name: "JSON", Type: vectordata.TypeJSON, Optional: true}}}
	c, err := factory(t).EnsureModelCollection(ctx, "array_sets", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{
		{Key: "a", Data: map[string]any{"Tags": []string{"x", "y"}, "JSON": map[string]any{"values": []any{"x", []string{"x"}, nil}}}},
		{Key: "b", Data: map[string]any{"Tags": []string{"y"}, "JSON": map[string]any{"values": []string{"y"}}}},
		{Key: "c", Data: map[string]any{"Tags": []string{}, "JSON": map[string]any{"values": []string{}}}},
		{Key: "d", Data: map[string]any{"JSON": map[string]any{"values": "x"}}},
		{Key: "e"},
	}); err != nil {
		t.Fatal(err)
	}
	native := vectordata.Column("Tags")
	path := vectordata.DataPath("JSON", "values")
	for _, tt := range []struct {
		filter vectordata.Filter
		want   []string
	}{
		{vectordata.ContainsAny(native, "x", "y"), []string{"a", "b"}},
		{vectordata.ContainsAll(native, "x", "y"), []string{"a"}},
		{vectordata.ContainsAll(native), []string{"a", "b", "c"}},
		{vectordata.Not(vectordata.ContainsAny(native)), []string{"a", "b", "c"}},
		{vectordata.ContainsAny(path, "x", nil), []string{"a"}},
		{vectordata.ContainsAll(path, "x", nil), []string{"a"}},
		{vectordata.ContainsAny(path, []string{"x"}), []string{"a"}},
		{vectordata.ContainsAll(path), []string{"a", "b", "c"}},
		{vectordata.ContainsAny(path), nil},
		{vectordata.Not(vectordata.ContainsAny(path)), []string{"a", "b", "c", "d", "e"}},
	} {
		rows, err := c.Query(ctx, tt.filter, 10, vectordata.QueryOptions{})
		if err != nil {
			t.Fatal(err)
		}
		keys := []string{}
		for _, r := range rows {
			keys = append(keys, r.Key.(string))
		}
		if !slices.Equal(keys, tt.want) {
			t.Fatalf("%#v: got %v want %v", tt.filter, keys, tt.want)
		}
	}
}
