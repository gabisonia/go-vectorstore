package conformance

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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
	instant := time.Date(2026, 7, 1, 9, 2, 3, 123456789, time.FixedZone("offset", 7200))
	canonical := time.Date(2026, 7, 1, 7, 2, 3, 123456000, time.UTC)
	for _, tt := range []struct {
		kind                                                         vectordata.DataType
		input, want, empty, null, other, needle, badArray, badNeedle any
	}{
		{vectordata.TypeInt64Array, arrayIntegers{1, 2, 2, -4}, []int64{1, 2, 2, -4}, []int64{}, []int64(nil), []int64{3, 4}, arrayInteger(2), []any{1, nil}, 1.5},
		{vectordata.TypeFloat64Array, []arrayFloat{0.25, -1.5, 0.25}, []float64{0.25, -1.5, 0.25}, []float64{}, []float64(nil), []float64{2.5, 3.5}, arrayFloat(0.25), []float64{math.NaN()}, "0.25"},
		{vectordata.TypeBoolArray, arrayFlags{true, false, true}, []bool{true, false, true}, []bool{}, []bool(nil), []bool{true}, arrayFlag(false), []any{true, 1}, 1},
		{vectordata.TypeUUIDArray, []nativeUUID{{strings.ToUpper(nativeUUIDA)}, {nativeUUIDA}}, []string{nativeUUIDA, nativeUUIDA}, []string{}, []nativeUUID(nil), []string{nativeUUIDB}, nativeUUID{strings.ToUpper(nativeUUIDA)}, []string{"invalid"}, "invalid"},
		{vectordata.TypeTimeArray, []arrayInstant{arrayInstant(instant), arrayInstant(instant.Add(100 * time.Nanosecond))}, []time.Time{canonical, canonical}, []time.Time{}, []time.Time(nil), []time.Time{canonical.Add(time.Hour)}, instant.In(time.FixedZone("other", -18000)), []time.Time{time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}, "not a timestamp"},
		{vectordata.TypeDateOnlyArray, []vectordata.DateOnly{"2024-02-29", "2024-02-29"}, []string{"2024-02-29", "2024-02-29"}, []string{}, []string(nil), []string{"2026-01-01"}, vectordata.DateOnly("2024-02-29"), []string{"2026-02-29"}, "today"},
		{vectordata.TypeTimeOnlyArray, []vectordata.TimeOnly{"09:02:03.123456789", "09:02:03.123456999"}, []string{"09:02:03.123456", "09:02:03.123456"}, []string{}, []string(nil), []string{"10:00:00"}, vectordata.TimeOnly("09:02:03.123456001"), []string{"24:00:00"}, "09:02:03Z"},
		{vectordata.TypeDecimalArray, decimalValues{"0.100", "1e-28", "0.100"}, []vectordata.Decimal{"0.1", "0.0000000000000000000000000001", "0.1"}, []vectordata.Decimal{}, []vectordata.Decimal(nil), []vectordata.Decimal{"2"}, decimalText("1e-1"), []any{1, nil}, 0.1},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			if !slices.Contains(caps.DataTypes, tt.kind) {
				t.Skip("array type is not advertised")
			}
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
				{"contains any", vectordata.ContainsAny(field, tt.needle, tt.needle), []string{"a"}},
				{"contains all", vectordata.ContainsAll(field, tt.needle, tt.needle), []string{"a"}},
				{"any empty", vectordata.ContainsAny(field), nil},
				{"all empty", vectordata.ContainsAll(field), []string{"a", "b", "d"}},
				{"not any empty", vectordata.Not(vectordata.ContainsAny(field)), []string{"a", "b", "d"}},
				{"not all empty", vectordata.Not(vectordata.ContainsAll(field)), nil},
				{"any null", vectordata.ContainsAny(field, nil, tt.needle), []string{"a"}},
				{"all null", vectordata.ContainsAll(field, nil, tt.needle), nil},
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
			for _, invalid := range []vectordata.Filter{vectordata.Contains(field, tt.badNeedle), vectordata.ContainsAny(field, tt.badNeedle), vectordata.ContainsAll(field, tt.badNeedle), vectordata.Eq(field, tt.badArray), vectordata.Gt(field, tt.want)} {
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
		for _, kind := range []vectordata.DataType{vectordata.TypeInt64Array, vectordata.TypeFloat64Array, vectordata.TypeBoolArray} {
			if !slices.Contains(caps.DataTypes, kind) {
				t.Skip("numeric arrays not advertised")
			}
		}
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
	t.Run("structured typed and legacy mappings", func(t *testing.T) { runStructuredArrayModels(t, factory, caps, instant, canonical) })
}

type arrayInstant time.Time
type arrayUUIDs []nativeUUID
type arrayInstants []arrayInstant
type structuredArrayModel struct {
	ID          string        `vectordata:"key"`
	UUIDs       arrayUUIDs    `vectordata:"data,type=uuid_array,optional,index"`
	Times       arrayInstants `vectordata:"data,type=time_array,optional,index"`
	Pointer     *arrayUUIDs   `vectordata:"data,type=uuid_array"`
	LegacyTimes []time.Time   `vectordata:"data"`
	LegacyUUIDs []nativeUUID  `vectordata:"data"`
}

func runStructuredArrayModels(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities, instant, canonical time.Time) {
	if !slices.Contains(caps.DataTypes, vectordata.TypeUUIDArray) || !slices.Contains(caps.DataTypes, vectordata.TypeTimeArray) {
		t.Skip("structured arrays not advertised")
	}
	ctx := context.Background()
	c, err := vectordata.EnsureTypedModelCollection[string, structuredArrayModel](ctx, factory(t), "structured_arrays", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	empty := arrayUUIDs{}
	input := structuredArrayModel{ID: "a", UUIDs: arrayUUIDs{{strings.ToUpper(nativeUUIDA)}}, Times: arrayInstants{arrayInstant(instant)}, Pointer: &empty, LegacyTimes: []time.Time{instant.UTC()}, LegacyUUIDs: []nativeUUID{{nativeUUIDA}}}
	if _, err := c.Insert(ctx, []structuredArrayModel{input, {ID: "null"}, {ID: "empty", UUIDs: arrayUUIDs{}, Times: arrayInstants{}}}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
	want := input
	want.UUIDs = arrayUUIDs{{nativeUUIDA}}
	want.Times = arrayInstants{arrayInstant(canonical)}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("structured native/legacy round trip: %+v, %v", got, err)
	}
	got, err = c.Get(ctx, "null", vectordata.RetrievalOptions{})
	if err != nil || got.UUIDs != nil || got.Times != nil || got.Pointer != nil {
		t.Fatalf("structured NULL arrays: %+v, %v", got, err)
	}
	got, err = c.Get(ctx, "empty", vectordata.RetrievalOptions{})
	if err != nil || got.UUIDs == nil || got.Times == nil || len(got.UUIDs)+len(got.Times) != 0 {
		t.Fatalf("structured empty arrays: %+v, %v", got, err)
	}
}
