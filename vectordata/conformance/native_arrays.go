package conformance

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type arrayInteger int16
type arrayIntegers []arrayInteger
type arrayFloat float32
type arrayFlag bool
type arrayFlags []arrayFlag

type nativeArrayModel struct {
	ID           string        `vectordata:"key"`
	Ints         arrayIntegers `vectordata:"data,type=int64_array,optional,index"`
	Floats       []float32     `vectordata:"data,type=float64_array,optional,index"`
	Flags        arrayFlags    `vectordata:"data,type=bool_array,optional,index"`
	LegacyInts   []int64       `vectordata:"data"`
	LegacyFloats []float64     `vectordata:"data"`
	LegacyFlags  []bool        `vectordata:"data"`
}

func runNativeArrays(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	for _, kind := range []vectordata.DataType{vectordata.TypeInt64Array, vectordata.TypeFloat64Array, vectordata.TypeBoolArray} {
		if !slices.Contains(caps.DataTypes, kind) {
			t.Skip("native arrays are not advertised")
		}
	}
	for _, tt := range []struct {
		kind                                                         vectordata.DataType
		input, want, empty, null, other, needle, badArray, badNeedle any
	}{
		{vectordata.TypeInt64Array, arrayIntegers{1, 2, 2, -4}, []int64{1, 2, 2, -4}, []int64{}, []int64(nil), []int64{3, 4}, arrayInteger(2), []any{1, nil}, 1.5},
		{vectordata.TypeFloat64Array, []arrayFloat{0.25, -1.5, 0.25}, []float64{0.25, -1.5, 0.25}, []float64{}, []float64(nil), []float64{2.5, 3.5}, arrayFloat(0.25), []float64{math.NaN()}, "0.25"},
		{vectordata.TypeBoolArray, arrayFlags{true, false, true}, []bool{true, false, true}, []bool{}, []bool(nil), []bool{true}, arrayFlag(false), []any{true, 1}, 1},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			ctx := context.Background()
			store := factory(t)
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Values", StorageName: `values";x`, Type: tt.kind, Optional: true, IsIndexed: true}, {Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}}
			c, err := store.EnsureModelCollection(ctx, "arrays", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			whole := reflect.MakeSlice(reflect.TypeOf(tt.input), reflect.ValueOf(tt.input).Len(), reflect.ValueOf(tt.input).Len())
			reflect.Copy(whole, reflect.ValueOf(tt.input))
			record := func(key string, value any) vectordata.ModelRecord {
				return vectordata.ModelRecord{Key: key, Data: map[string]any{"Values": value, "Body": "alpha"}, Vectors: map[string][]float32{"Vector": {1, 0}}}
			}
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{record("a", tt.input), record("b", tt.empty), record("c", tt.null), record("d", tt.other), record("e", nil)}); err != nil {
				t.Fatal(err)
			}
			reflect.ValueOf(tt.input).Index(0).Set(reflect.Zero(reflect.TypeOf(tt.input).Elem()))
			row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
			if err != nil || !reflect.DeepEqual(row.Data["Values"], tt.want) {
				t.Fatalf("native array round trip/copy: %v, %v", row, err)
			}
			reflect.ValueOf(row.Data["Values"]).Index(0).Set(reflect.Zero(reflect.TypeOf(row.Data["Values"]).Elem()))
			row, err = c.Get(ctx, "a", vectordata.RetrievalOptions{})
			if err != nil || !reflect.DeepEqual(row.Data["Values"], tt.want) {
				t.Fatalf("read aliases storage: %v, %v", row, err)
			}
			field := vectordata.Column("Values")
			for _, test := range []struct {
				name   string
				filter vectordata.Filter
				keys   []string
			}{
				{"whole equality with alias", vectordata.Eq(field, whole.Interface()), []string{"a"}},
				{"empty", vectordata.Eq(field, tt.empty), []string{"b"}},
				{"null", vectordata.Eq(field, nil), []string{"c", "e"}},
				{"typed null", vectordata.Eq(field, tt.null), []string{"c", "e"}},
				{"exists", vectordata.Exists(field), []string{"a", "b", "d"}},
				{"contains alias", vectordata.Contains(field, tt.needle), []string{"a"}},
				{"negated contains", vectordata.Not(vectordata.Contains(field, tt.needle)), []string{"b", "d"}},
				{"contains null", vectordata.Contains(field, nil), nil},
				{"negated null membership", vectordata.Not(vectordata.Contains(field, nil)), []string{"a", "b", "d"}},
				{"array membership", vectordata.In(field, whole.Interface(), tt.empty, nil), []string{"a", "b"}},
				{"negated membership with null", vectordata.Not(vectordata.In(field, whole.Interface(), nil)), nil},
				{"nested", vectordata.Or(vectordata.Contains(field, tt.needle), vectordata.Eq(field, tt.empty)), []string{"a", "b"}},
			} {
				t.Run(test.name, func(t *testing.T) {
					got, err := c.Query(ctx, test.filter, 10, vectordata.QueryOptions{})
					if err != nil {
						t.Fatal(err)
					}
					keys := make([]string, len(got))
					for i := range got {
						keys[i] = got[i].Key.(string)
					}
					slices.Sort(keys)
					if !slices.Equal(keys, test.keys) {
						t.Fatalf("keys=%v want=%v", keys, test.keys)
					}
					if count, err := c.Count(ctx, test.filter); err != nil || count != int64(len(test.keys)) {
						t.Fatalf("count=%d error=%v", count, err)
					}
				})
			}
			for _, invalid := range []vectordata.Filter{vectordata.Contains(field, tt.badNeedle), vectordata.Eq(field, tt.badArray), vectordata.Gt(field, tt.want)} {
				if _, err := c.Query(ctx, invalid, 1, vectordata.QueryOptions{}); !errors.Is(err, vectordata.ErrInvalidFilter) {
					t.Fatalf("invalid array filter accepted: %v", err)
				}
			}
			if _, err := c.Query(ctx, nil, 1, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "Values"}}}); !errors.Is(err, vectordata.ErrUnsupported) {
				t.Fatalf("portable array ordering: %v", err)
			}
			filter := vectordata.Contains(field, tt.needle)
			matches, err := c.Search(ctx, []float32{1, 0}, 5, vectordata.ModelSearchOptions{Filter: filter})
			if err != nil || len(matches) != 1 || matches[0].Record.Key != "a" || !reflect.DeepEqual(matches[0].Record.Data["Values"], tt.want) {
				t.Fatalf("array-filtered vector search: %v, %v", matches, err)
			}
			if caps.HybridSearch {
				matches, err := c.(vectordata.HybridSearcher).HybridSearch(ctx, []float32{1, 0}, "alpha", 5, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{Filter: filter}})
				if err != nil || len(matches) != 1 || matches[0].Record.Key != "a" {
					t.Fatalf("array-filtered hybrid search: %v, %v", matches, err)
				}
			}
			if _, err := c.Upsert(ctx, []vectordata.ModelRecord{record("new", tt.empty), record("a", tt.badArray)}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("invalid array batch: %v", err)
			}
			if count, err := c.Count(ctx, nil); err != nil || count != 5 {
				t.Fatalf("partial array write: %d, %v", count, err)
			}
			row, err = c.Get(ctx, "a", vectordata.RetrievalOptions{})
			if err != nil || !reflect.DeepEqual(row.Data["Values"], tt.want) {
				t.Fatalf("invalid batch overwrote stored array: %v, %v", row, err)
			}
		})
	}
	t.Run("typed and legacy mappings", func(t *testing.T) {
		ctx := context.Background()
		c, err := vectordata.EnsureTypedModelCollection[string, nativeArrayModel](ctx, factory(t), "typed_arrays", nil, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		input := nativeArrayModel{ID: "a", Ints: arrayIntegers{1, -2}, Floats: []float32{0.25, -1.5}, Flags: arrayFlags{true, false}, LegacyInts: []int64{9007199254740993}, LegacyFloats: []float64{0.25}, LegacyFlags: []bool{true}}
		if _, err := c.Insert(ctx, []nativeArrayModel{input, {ID: "null"}, {ID: "empty", Ints: arrayIntegers{}, Floats: []float32{}, Flags: arrayFlags{}}}); err != nil {
			t.Fatal(err)
		}
		got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
		if err != nil || !reflect.DeepEqual(got, input) {
			t.Fatalf("typed array/legacy round trip: %+v, %v", got, err)
		}
		got, err = c.Get(ctx, "null", vectordata.RetrievalOptions{})
		if err != nil || got.Ints != nil || got.Floats != nil || got.Flags != nil {
			t.Fatalf("typed NULL arrays: %+v, %v", got, err)
		}
		got, err = c.Get(ctx, "empty", vectordata.RetrievalOptions{})
		if err != nil || got.Ints == nil || got.Floats == nil || got.Flags == nil || len(got.Ints)+len(got.Floats)+len(got.Flags) != 0 {
			t.Fatalf("typed empty arrays: %+v, %v", got, err)
		}
	})
}
