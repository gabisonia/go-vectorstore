package conformance

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

var errNativeJSON = errors.New("native value used application JSON decoding")

type nativeReadInt int16
type nativeReadUint uint8
type nativeReadFloat float32
type nativeReadBool bool
type nativeReadString string
type nativeReadFloats []nativeReadFloat

func (*nativeReadInt) UnmarshalJSON([]byte) error    { return errNativeJSON }
func (*nativeReadUint) UnmarshalJSON([]byte) error   { return errNativeJSON }
func (*nativeReadFloat) UnmarshalJSON([]byte) error  { return errNativeJSON }
func (*nativeReadBool) UnmarshalJSON([]byte) error   { return errNativeJSON }
func (*nativeReadString) UnmarshalJSON([]byte) error { return errNativeJSON }
func (*nativeReadFloats) UnmarshalJSON([]byte) error { return errNativeJSON }

type nativeReadModel struct {
	ID      string             `vectordata:"key"`
	Ints    []nativeReadInt    `vectordata:"data,type=int64_array,optional"`
	Uints   []nativeReadUint   `vectordata:"data,type=int64_array,optional"`
	Floats  nativeReadFloats   `vectordata:"data,type=float64_array,optional"`
	Flags   []nativeReadBool   `vectordata:"data,type=bool_array,optional"`
	Labels  []nativeReadString `vectordata:"data"`
	Enabled nativeReadBool     `vectordata:"data"`
	Pointer *[]nativeReadInt   `vectordata:"data,type=int64_array"`
	Vector  []float32          `vectordata:"vector,dimensions=2,metric=l2"`
}

func runNativeDecode(t *testing.T, factory Factory) {
	ctx := t.Context()
	store := factory(t)
	c, err := vectordata.EnsureTypedModelCollection[string, nativeReadModel](ctx, store, "native_decode", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	valid := func(id string) nativeReadModel {
		return nativeReadModel{ID: id, Ints: []nativeReadInt{math.MinInt16, math.MaxInt16}, Uints: []nativeReadUint{0, math.MaxUint8}, Floats: nativeReadFloats{0.1, math.MaxFloat32}, Flags: []nativeReadBool{true, false}, Labels: []nativeReadString{"hello"}, Enabled: true, Vector: []float32{1, 0}}
	}
	first := valid("a")
	pointer := []nativeReadInt{1}
	first.Pointer = &pointer
	emptyPointer := []nativeReadInt{}
	empty := nativeReadModel{ID: "empty", Ints: []nativeReadInt{}, Uints: []nativeReadUint{}, Floats: nativeReadFloats{}, Flags: []nativeReadBool{}, Labels: []nativeReadString{}, Pointer: &emptyPointer, Vector: []float32{1, 0}}
	null := nativeReadModel{ID: "null", Labels: []nativeReadString{}, Vector: []float32{1, 0}}
	if _, err := c.Insert(ctx, []nativeReadModel{first, valid("b"), valid("zz"), empty, null}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []nativeReadModel{first, empty, null} {
		got, err := c.Get(ctx, want.ID, vectordata.RetrievalOptions{IncludeVectors: true})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("native values/NULL/empty round trip: %+v, %v; want %+v", got, err, want)
		}
	}
	got, err := c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil {
		t.Fatal(err)
	}
	got.Ints[0] = 0
	got, err = c.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || got.Ints[0] != math.MinInt16 {
		t.Fatalf("native decoding aliases storage: %+v, %v", got, err)
	}
	if _, err := c.UpsertOne(ctx, got); err != nil {
		t.Fatal(err)
	}
	base, err := store.GetModelCollection(c.Name(), c.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := base.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || !reflect.DeepEqual(canonical.Data["Floats"], []float64{float64(float32(0.1)), math.MaxFloat32}) {
		t.Fatalf("native reread/rewrite changed canonical floats: %+v, %v", canonical, err)
	}
	bad := canonical
	bad.Key = "z"
	for _, tt := range []struct {
		field string
		value any
	}{
		{"Ints", []int64{1, math.MaxInt16 + 1}},
		{"Uints", []int64{1, -1}},
		{"Uints", []int64{1, math.MaxUint8 + 1}},
		{"Floats", []float64{1, math.MaxFloat64}},
	} {
		original := bad.Data[tt.field]
		bad.Data[tt.field] = tt.value
		if _, err := base.Upsert(ctx, []vectordata.ModelRecord{bad}); err != nil {
			t.Fatal(err)
		}
		got, err := c.Get(ctx, "z", vectordata.RetrievalOptions{})
		if err == nil || errors.Is(err, errNativeJSON) || !strings.Contains(err.Error(), tt.field) || !reflect.DeepEqual(got, nativeReadModel{}) {
			t.Fatalf("native range check lost/partial model: %+v, %v", got, err)
		}
		bad.Data[tt.field] = original
	}
	rows, err := c.GetMany(ctx, []string{"a", "z"}, vectordata.RetrievalOptions{})
	if err == nil || rows != nil {
		t.Fatalf("batch read returned partial models: %+v, %v", rows, err)
	}
	rows, err = c.Query(ctx, nil, 6, vectordata.QueryOptions{})
	if err == nil || rows != nil {
		t.Fatalf("query returned partial models: %+v, %v", rows, err)
	}
	hits, err := c.Search(ctx, []float32{1, 0}, 6, vectordata.ModelSearchOptions{})
	if err == nil || hits != nil {
		t.Fatalf("search returned partial models: %+v, %v", hits, err)
	}
	values, failures := 0, 0
	for row, err := range c.QueryIter(ctx, nil, 6, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "ID"}}}) {
		if err != nil {
			if !strings.Contains(err.Error(), "Floats") || !reflect.DeepEqual(row, nativeReadModel{}) {
				t.Fatalf("iterator lost field error/returned partial model: %+v, %v", row, err)
			}
			failures++
			continue
		}
		values++
	}
	if values != 4 || failures != 1 {
		t.Fatalf("iterator did not stop at range error: values=%d failures=%d", values, failures)
	}
	if count, err := c.Count(ctx, nil); err != nil || count != 6 {
		t.Fatalf("failed reads changed storage or leaked cursor: %d, %v", count, err)
	}
	reopened, err := vectordata.GetTypedModelCollection[string, nativeReadModel](store, c.Name(), nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err = reopened.Get(ctx, "a", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("native decoding after reopen: %+v, %v", got, err)
	}
}
