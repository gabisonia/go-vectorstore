package conformance

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// runFilterEdges locks down expected semantics independently of provider parity.
func runFilterEdges(t *testing.T, factory Factory) {
	ctx := context.Background()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Details", Type: vectordata.TypeJSON, Optional: true}, {Name: "Tags", Type: vectordata.TypeStringArray, Optional: true}}}
	c, err := factory(t).EnsureModelCollection(ctx, "filter_edges", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	records := []vectordata.ModelRecord{
		{Key: "a", Data: map[string]any{"Details": map[string]any{"value": "2", "arr": []any{nil, 2}, "items": []any{"first", nil}}, "Tags": nil}},
		{Key: "b", Data: map[string]any{"Details": map[string]any{"value": 2, "arr": []any{nil, "2"}, "items": []any{"first", nil}}, "Tags": []string{}}},
		{Key: "c", Data: map[string]any{"Details": map[string]any{}, "Tags": []string{"news"}}},
		{Key: "d", Data: map[string]any{"Details": vectordata.JSONNull{}, "Tags": nil}},
		{Key: "e", Data: map[string]any{"Details": nil, "Tags": []string{"other"}}},
		{Key: "f", Data: map[string]any{"Details": json.RawMessage(`{"value":null,"arr":[{"n":2}]}`), "Tags": []string{"news"}}},
	}
	if _, err := c.Insert(ctx, records); err != nil {
		t.Fatal(err)
	}
	value := vectordata.DataPath("Details", "value")
	tests := []struct {
		name   string
		filter vectordata.Filter
		want   []string
	}{
		{"JSON number differs from string", vectordata.Eq(value, json.Number("2")), []string{"b"}},
		{"JSON string differs from number", vectordata.Eq(value, "2"), []string{"a"}},
		{"JSON null marker at path", vectordata.Eq(value, vectordata.JSONNull{}), []string{"f"}},
		{"JSON null marker at root", vectordata.Eq(vectordata.DataPath("Details"), vectordata.JSONNull{}), []string{"d"}},
		{"typed nil native JSON means SQL null", vectordata.Eq(vectordata.Column("Details"), map[string]any(nil)), []string{"e"}},
		{"native JSON IN typed null remains unknown", vectordata.In(vectordata.Column("Details"), map[string]any(nil)), nil},
		{"native JSON IN distinguishes JSON null", vectordata.In(vectordata.Column("Details"), map[string]any(nil), vectordata.JSONNull{}), []string{"d"}},
		{"typed nil native array means SQL null", vectordata.Eq(vectordata.Column("Tags"), []string(nil)), []string{"a", "d"}},
		{"empty array differs from SQL null", vectordata.Eq(vectordata.Column("Tags"), []string{}), []string{"b"}},
		{"native IN null remains unknown", vectordata.In(vectordata.Column("Tags"), []string(nil), []string{}), []string{"b"}},
		{"negated native membership keeps unknown", vectordata.Not(vectordata.Contains(vectordata.Column("Tags"), "news")), []string{"b", "e"}},
		{"JSON membership normalizes null marker", vectordata.Contains(vectordata.DataPath("Details", "arr"), vectordata.JSONNull{}), []string{"a", "b"}},
		{"JSON equality normalizes raw payload", vectordata.Eq(value, json.RawMessage(`"2"`)), []string{"a"}},
		{"JSON equality normalizes typed nil", vectordata.Eq(value, (*string)(nil)), []string{"f"}},
		{"JSON membership normalizes raw payload", vectordata.Contains(vectordata.DataPath("Details", "arr"), json.RawMessage(`{"n":2}`)), []string{"f"}},
		{"leading array index whitespace", vectordata.Exists(vectordata.DataPath("Details", "items", " 1")), []string{"a", "b"}},
		{"trailing array index whitespace", vectordata.Exists(vectordata.DataPath("Details", "items", "1 ")), nil},
		{"signed array index", vectordata.Eq(vectordata.DataPath("Details", "items", "+1"), nil), []string{"a", "b"}},
		{"non ASCII array index whitespace", vectordata.Exists(vectordata.DataPath("Details", "items", "\u00a01")), nil},
		{"missing JSON text comparison stays unknown", vectordata.Not(vectordata.Gt(value, "1")), nil},
		{"missing JSON numeric comparison is false", vectordata.Not(vectordata.Gt(value, 1)), []string{"c", "d", "e", "f"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := c.Query(ctx, tt.filter, 10, vectordata.QueryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = r.Key.(string)
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("keys=%v, want=%v", got, tt.want)
			}
		})
	}
}
