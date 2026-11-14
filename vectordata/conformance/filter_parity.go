package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// RunFilterParity compares generated filter trees against two real providers.
// Fixed seeds make failures reproducible; Query and Count are compared for every
// case, with Search checked on a subset using an unindexed vector property.
func RunFilterParity(t *testing.T, left, right Factory) {
	t.Helper()
	for _, seed := range []int64{1, 42, 20261114} {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			definition, records, leaves := filterCorpus()
			collections := make([]vectordata.ModelCollection, 2)
			for i, factory := range []Factory{left, right} {
				var err error
				collections[i], err = factory(t).EnsureModelCollection(ctx, "filter_parity", definition, vectordata.ModelCollectionOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = collections[i].Insert(ctx, records); err != nil {
					t.Fatal(err)
				}
			}
			rng := rand.New(rand.NewSource(seed))
			var tree func(int) vectordata.Filter
			tree = func(depth int) vectordata.Filter {
				if depth == 0 || rng.Intn(3) == 0 {
					return leaves[rng.Intn(len(leaves))]
				}
				switch rng.Intn(3) {
				case 0:
					return vectordata.Not(tree(depth - 1))
				case 1:
					return vectordata.And(tree(depth-1), tree(depth-1))
				default:
					return vectordata.Or(tree(depth-1), tree(depth-1))
				}
			}
			filters := append([]vectordata.Filter(nil), leaves...)
			for range 120 {
				filters = append(filters, tree(3))
			}
			for index, filter := range filters {
				t.Run(fmt.Sprintf("case_%03d", index), func(t *testing.T) {
					var keys [2][]string
					for i, c := range collections {
						rows, err := c.Query(ctx, filter, len(records)+1, vectordata.QueryOptions{})
						if err != nil {
							t.Fatalf("provider %d Query(%#v): %v", i, filter, err)
						}
						keys[i] = make([]string, len(rows))
						for j, r := range rows {
							keys[i][j] = r.Key.(string)
						}
						slices.Sort(keys[i])
						count, err := c.Count(ctx, filter)
						if err != nil || count != int64(len(rows)) {
							t.Fatalf("provider %d Count(%#v)=%d, query=%d, error=%v", i, filter, count, len(rows), err)
						}
						if index%10 == 0 {
							found, err := c.Search(ctx, []float32{1, 0}, len(records)+1, vectordata.ModelSearchOptions{Filter: filter})
							if err != nil {
								t.Fatalf("provider %d Search(%#v): %v", i, filter, err)
							}
							searchKeys := make([]string, len(found))
							for j, r := range found {
								searchKeys[j] = r.Record.Key.(string)
							}
							slices.Sort(searchKeys)
							if !slices.Equal(searchKeys, keys[i]) {
								t.Fatalf("provider %d Search(%#v) keys=%v, query=%v", i, filter, searchKeys, keys[i])
							}
						}
					}
					if !slices.Equal(keys[0], keys[1]) {
						t.Fatalf("filter %#v: left=%v, right=%v", filter, keys[0], keys[1])
					}
				})
			}
		})
	}
}

func filterCorpus() (vectordata.CollectionDefinition, []vectordata.ModelRecord, []vectordata.Filter) {
	d := vectordata.CollectionDefinition{
		Key: vectordata.KeyProperty{Name: "ID"},
		Data: []vectordata.DataProperty{
			{Name: "Number", Type: vectordata.TypeInt64, Optional: true},
			{Name: "Price", Type: vectordata.TypeFloat64, Optional: true},
			{Name: "Active", Type: vectordata.TypeBool, Optional: true},
			{Name: "Text", Type: vectordata.TypeString, Optional: true},
			{Name: "Tags", Type: vectordata.TypeStringArray, Optional: true},
			{Name: "Details", Type: vectordata.TypeJSON, Optional: true},
		},
		Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}},
	}
	values := []any{nil, json.Number("9007199254740993"), json.Number("0.1234567890123456789"), json.Number("-2"), "2", "1e999", "1e999999999", "NaN", true, []any{nil, "news", json.Number("2"), map[string]any{"a": 1}}, map[string]any{"nested": nil}}
	records := make([]vectordata.ModelRecord, 48)
	for i := range records {
		data := map[string]any{}
		if i%4 != 0 {
			data["Number"] = int64(i%7 - 3)
			data["Price"] = float64(i%5) / 2
			data["Active"] = i%2 == 0
			data["Text"] = []string{"", "a", "b", "news"}[i%4]
			data["Tags"] = []string{"news", fmt.Sprintf("tag%d", i%3)}
		}
		switch i % 12 {
		case 0:
			data["Details"] = nil
		case 1:
			data["Details"] = vectordata.JSONNull{}
		case 2:
			data["Details"] = []any{nil, 1, "news", map[string]any{"a": 1}}
		case 3:
			data["Details"] = map[string]any{}
		default:
			data["Details"] = map[string]any{"value": values[i%len(values)], "text": []any{nil, "a", "news", "2"}[i%4], "arr": []any{nil, "news", map[string]any{"a": 1}, json.Number("9007199254740993")}, "": map[string]any{" ": "literal"}, "items": []any{"first", nil, "last"}}
		}
		records[i] = vectordata.ModelRecord{Key: fmt.Sprintf("row%02d", i), Data: data, Vectors: map[string][]float32{"Vector": {1, float32(i+1) / 48}}}
	}
	var leaves []vectordata.Filter
	for _, property := range []string{"Number", "Price", "Active", "Text", "Tags", "Details"} {
		f := vectordata.Column(property)
		leaves = append(leaves, vectordata.Exists(f), vectordata.Eq(f, nil), vectordata.Not(vectordata.Eq(f, nil)))
	}
	for _, value := range []any{int64(-2), int64(0), int64(2), int64(9007199254740993)} {
		f := vectordata.Column("Number")
		leaves = append(leaves, vectordata.Eq(f, value), vectordata.Gt(f, value), vectordata.Gte(f, value), vectordata.Lt(f, value), vectordata.Lte(f, value), vectordata.In(f, value, nil))
	}
	leaves = append(leaves, vectordata.Eq(vectordata.Column("Price"), float32(0.5)), vectordata.Eq(vectordata.Column("Active"), true), vectordata.In(vectordata.Column("Text"), "news", nil), vectordata.Contains(vectordata.Column("Tags"), "news"), vectordata.Eq(vectordata.Column("Tags"), []string{"news", "tag1"}))
	for _, path := range [][]string{nil, {"value"}, {"missing"}, {"text"}, {"arr"}, {"arr", "0"}, {"items", "-1"}, {"items", " 1 "}, {"", " "}} {
		f := vectordata.DataPath("Details", path...)
		leaves = append(leaves, vectordata.Exists(f))
		for _, value := range []any{nil, vectordata.JSONNull{}, json.Number("9007199254740993"), json.Number("2"), "2", []any{nil, "news"}, map[string]any{"a": 1}} {
			leaves = append(leaves, vectordata.Eq(f, value), vectordata.Not(vectordata.Eq(f, value)), vectordata.In(f, value, nil))
		}
	}
	for _, value := range []any{json.Number("2"), json.Number("9007199254740992"), json.Number("0.1234567890123456788")} {
		f := vectordata.DataPath("Details", "value")
		leaves = append(leaves, vectordata.Gt(f, value), vectordata.Gte(f, value), vectordata.Lt(f, value), vectordata.Lte(f, value))
	}
	text := vectordata.DataPath("Details", "text")
	leaves = append(leaves, vectordata.Gt(text, "a"), vectordata.Not(vectordata.Gt(text, "a")), vectordata.Lte(text, "news"))
	for _, value := range []any{nil, vectordata.JSONNull{}, "news", map[string]any{"a": 1}, json.Number("9007199254740993")} {
		f := vectordata.DataPath("Details", "arr")
		leaves = append(leaves, vectordata.Contains(f, value), vectordata.Not(vectordata.Contains(f, value)))
	}
	return d, records, leaves
}
