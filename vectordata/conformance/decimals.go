package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type decimalText string
type decimalValues []decimalText
type decimalModel struct {
	ID            string              `vectordata:"key"`
	Price         vectordata.Decimal  `vectordata:"data,type=decimal,index"`
	Backup        *vectordata.Decimal `vectordata:"data,type=decimal"`
	Values        decimalValues       `vectordata:"data,type=decimal_array,optional,index"`
	Legacy        string              `vectordata:"data"`
	LegacyNumbers []float64           `vectordata:"data"`
	LegacyJSON    json.Number         `vectordata:"data,type=json"`
}

func runDecimals(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !slices.Contains(caps.DataTypes, vectordata.TypeDecimal) {
		t.Skip("decimals not advertised")
	}
	ctx := context.Background()
	store := factory(t)
	c, err := store.EnsureModelCollection(ctx, "decimals", vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID"},
		Data:    []vectordata.DataProperty{{Name: "Price", StorageName: `price";x`, Type: vectordata.TypeDecimal, Optional: true, IsIndexed: true}, {Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}, {Name: "Details", Type: vectordata.TypeJSON}},
		Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}},
	}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const exact = "9007199254740993.000000001"
	record := func(key string, value any) vectordata.ModelRecord {
		return vectordata.ModelRecord{Key: key, Data: map[string]any{"Price": value, "Body": "alpha", "Details": map[string]any{"value": json.Number(exact), "text": exact}}, Vectors: map[string][]float32{"Vector": {1, 0}}}
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{record("a", "-79228162514264337593543950335"), record("b", decimalText("2.00")), record("c", 10), record("d", json.Number(exact)), record("e", "79228162514264337593543950335"), record("f", nil)}); err != nil {
		t.Fatal(err)
	}
	row, err := c.Get(ctx, "d", vectordata.RetrievalOptions{})
	if err != nil || row.Data["Price"] != vectordata.Decimal(exact) {
		t.Fatalf("exact decimal round trip: %v, %v", row, err)
	}
	field := vectordata.Column("Price")
	for _, tt := range []struct {
		name   string
		filter vectordata.Filter
		keys   []string
	}{
		{"canonical equality", vectordata.Eq(field, "2e0"), []string{"b"}},
		{"exact fraction", vectordata.Eq(field, json.Number(exact)), []string{"d"}},
		{"adjacent fraction", vectordata.Eq(field, "9007199254740993.000000002"), nil},
		{"membership", vectordata.In(field, 2, json.Number(exact), nil), []string{"b", "d"}},
		{"negated membership null", vectordata.Not(vectordata.In(field, 2, nil)), nil},
		{"greater", vectordata.Gt(field, decimalText("2")), []string{"c", "d", "e"}},
		{"greater equal", vectordata.Gte(field, 10), []string{"c", "d", "e"}},
		{"less", vectordata.Lt(field, "10.000"), []string{"a", "b"}},
		{"less equal", vectordata.Lte(field, 2), []string{"a", "b"}},
		{"null", vectordata.Eq(field, (*vectordata.Decimal)(nil)), []string{"f"}},
		{"exists", vectordata.Exists(field), []string{"a", "b", "c", "d", "e"}},
		{"negated equality", vectordata.Not(vectordata.Eq(field, 2)), []string{"a", "c", "d", "e"}},
		{"JSON numeric greater", vectordata.Gt(vectordata.DataPath("Details", "value"), vectordata.Decimal("9007199254740993")), []string{"a", "b", "c", "d", "e", "f"}},
		{"JSON numeric lower", vectordata.Lt(vectordata.DataPath("Details", "value"), vectordata.Decimal("9007199254740993.000000002")), []string{"a", "b", "c", "d", "e", "f"}},
		{"JSON numeric excludes greater", vectordata.Gt(vectordata.DataPath("Details", "value"), vectordata.Decimal("9007199254740993.000000002")), nil},
		{"JSON numeric equality", vectordata.Eq(vectordata.DataPath("Details", "value"), vectordata.Decimal(exact)), []string{"a", "b", "c", "d", "e", "f"}},
		{"JSON string stays distinct", vectordata.Eq(vectordata.DataPath("Details", "text"), vectordata.Decimal(exact)), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := c.Query(ctx, tt.filter, 10, vectordata.QueryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			keys := make([]string, len(rows))
			for i, row := range rows {
				keys[i] = row.Key.(string)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, tt.keys) {
				t.Fatalf("keys=%v want=%v", keys, tt.keys)
			}
			if n, err := c.Count(ctx, tt.filter); err != nil || n != int64(len(tt.keys)) {
				t.Fatalf("decimal count: %d, %v", n, err)
			}
		})
	}
	for _, desc := range []bool{false, true} {
		rows, err := c.Query(ctx, nil, 6, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "Price", Descending: desc}}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"a", "b", "c", "d", "e", "f"}
		if desc {
			want = []string{"e", "d", "c", "b", "a", "f"}
		}
		if len(rows) != len(want) {
			t.Fatalf("decimal ordering rows: %v", rows)
		}
		for i, row := range rows {
			if row.Key != want[i] {
				t.Fatalf("decimal order: %v", rows)
			}
		}
	}
	for _, value := range []any{0.1, "1e-29", "79228162514264337593543950336", "NaN", "Infinity"} {
		if _, err := c.Query(ctx, vectordata.Eq(field, value), 1, vectordata.QueryOptions{}); !errors.Is(err, vectordata.ErrInvalidFilter) {
			t.Fatalf("invalid decimal filter: %v", err)
		}
	}
	if _, err := c.Query(ctx, vectordata.Gt(vectordata.DataPath("Details", "value"), vectordata.Decimal("NaN")), 1, vectordata.QueryOptions{}); !errors.Is(err, vectordata.ErrInvalidFilter) {
		t.Fatalf("invalid JSON decimal filter: %v", err)
	}
	filter := vectordata.Eq(field, json.Number(exact))
	if hits, err := c.Search(ctx, []float32{1, 0}, 5, vectordata.ModelSearchOptions{Filter: filter}); err != nil || len(hits) != 1 || hits[0].Record.Key != "d" {
		t.Fatalf("decimal vector filter: %v, %v", hits, err)
	}
	if caps.HybridSearch {
		if hits, err := c.(vectordata.HybridSearcher).HybridSearch(ctx, []float32{1, 0}, "alpha", 5, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{Filter: filter}}); err != nil || len(hits) != 1 || hits[0].Record.Key != "d" {
			t.Fatalf("decimal hybrid filter: %v, %v", hits, err)
		}
	}
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{record("new", "0.1"), record("d", "1e-29")}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("invalid decimal batch: %v", err)
	}
	if n, err := c.Count(ctx, nil); err != nil || n != 6 {
		t.Fatalf("partial decimal write: %d, %v", n, err)
	}
	row, err = c.Get(ctx, "d", vectordata.RetrievalOptions{})
	if err != nil || row.Data["Price"] != vectordata.Decimal(exact) {
		t.Fatalf("invalid batch changed price: %v, %v", row, err)
	}
	t.Run("typed and legacy", func(t *testing.T) {
		if !slices.Contains(caps.DataTypes, vectordata.TypeDecimalArray) {
			t.Skip("decimal arrays not advertised")
		}
		typed, err := vectordata.EnsureTypedModelCollection[string, decimalModel](ctx, store, "typed_decimals", nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		backup := vectordata.Decimal("1e-28")
		input := decimalModel{ID: "a", Price: "9007199254740993.000000001", Backup: &backup, Values: decimalValues{"1.2300", "1e-28"}, Legacy: "not a decimal", LegacyNumbers: []float64{0.1}, LegacyJSON: json.Number(exact)}
		if _, err := typed.Insert(ctx, []decimalModel{input, {ID: "null", Price: "0", LegacyJSON: "1"}, {ID: "empty", Price: "0", Values: decimalValues{}, LegacyJSON: "1"}}); err != nil {
			t.Fatal(err)
		}
		want := input
		normalized := vectordata.Decimal("0.0000000000000000000000000001")
		want.Backup = &normalized
		want.Values = decimalValues{"1.23", "0.0000000000000000000000000001"}
		got, err := typed.Get(ctx, "a", vectordata.RetrievalOptions{})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("typed decimal/legacy round trip: %+v, %v", got, err)
		}
		got, err = typed.Get(ctx, "null", vectordata.RetrievalOptions{})
		if err != nil || got.Backup != nil || got.Values != nil {
			t.Fatalf("typed decimal NULL: %+v, %v", got, err)
		}
		got, err = typed.Get(ctx, "empty", vectordata.RetrievalOptions{})
		if err != nil || got.Values == nil || len(got.Values) != 0 {
			t.Fatalf("typed decimal empty: %+v, %v", got, err)
		}
	})
}
