package vectordata_test

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type arrayInt int32
type arrayBool bool
type arrayInts []arrayInt

func TestNativeArrayNormalization(t *testing.T) {
	for _, tt := range []struct {
		kind        vectordata.DataType
		input, want any
	}{
		{vectordata.TypeInt64Array, arrayInts{1, -2}, []int64{1, -2}},
		{vectordata.TypeInt64Array, []uint64{math.MaxInt64}, []int64{math.MaxInt64}},
		{vectordata.TypeFloat64Array, []float32{0.25, -1.5}, []float64{0.25, -1.5}},
		{vectordata.TypeFloat64Array, []int64{1, -2}, []float64{1, -2}},
		{vectordata.TypeBoolArray, []arrayBool{false, true}, []bool{false, true}},
		{vectordata.TypeInt64Array, []any{int8(1), int64(2)}, []int64{1, 2}},
		{vectordata.TypeInt64Array, []int32{}, []int64{}},
		{vectordata.TypeFloat64Array, []float32{}, []float64{}},
		{vectordata.TypeBoolArray, []bool{}, []bool{}},
	} {
		got, err := vectordata.NormalizeModelData(tt.input, vectordata.DataProperty{Name: "Values", Type: tt.kind})
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%s normalization: got=%v want=%v error=%v", tt.kind, got, tt.want, err)
		}
		if reflect.ValueOf(tt.input).Len() > 0 {
			reflect.ValueOf(tt.input).Index(0).Set(reflect.Zero(reflect.TypeOf(tt.input).Elem()))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatal("normalized array aliases input")
			}
		}
	}
	for _, tt := range []struct {
		kind     vectordata.DataType
		nilSlice any
	}{{vectordata.TypeInt64Array, []int64(nil)}, {vectordata.TypeFloat64Array, []float64(nil)}, {vectordata.TypeBoolArray, []bool(nil)}} {
		for _, value := range []any{nil, tt.nilSlice} {
			if got, err := vectordata.NormalizeModelData(value, vectordata.DataProperty{Type: tt.kind, Optional: true}); err != nil || got != nil {
				t.Fatalf("optional null: %v, %v", got, err)
			}
			if _, err := vectordata.NormalizeModelData(value, vectordata.DataProperty{Type: tt.kind}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("required null accepted: %v", err)
			}
		}
	}
}

func TestNativeArrayInvalidValues(t *testing.T) {
	for _, tt := range []struct {
		kind  vectordata.DataType
		input any
	}{
		{vectordata.TypeInt64Array, []uint64{math.MaxUint64}}, {vectordata.TypeInt64Array, []float64{1.5}},
		{vectordata.TypeInt64Array, []string{}}, {vectordata.TypeInt64Array, [1]int64{1}},
		{vectordata.TypeInt64Array, [][]int64{{1}}}, {vectordata.TypeInt64Array, []any{1, nil}},
		{vectordata.TypeFloat64Array, []float64{math.NaN()}}, {vectordata.TypeFloat64Array, []float64{math.Inf(1)}},
		{vectordata.TypeFloat64Array, []float64{math.Inf(-1)}}, {vectordata.TypeBoolArray, []any{true, 1}},
	} {
		if _, err := vectordata.NormalizeModelData(tt.input, vectordata.DataProperty{Type: tt.kind}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("accepted %s %v: %v", tt.kind, tt.input, err)
		}
	}
}

func TestNativeArrayCodecAndLegacyInference(t *testing.T) {
	type model struct {
		ID           string      `vectordata:"key"`
		Ints         arrayInts   `vectordata:"data,type=int64_array"`
		Floats       []float32   `vectordata:"data,type=float64_array"`
		Flags        []arrayBool `vectordata:"data,type=bool_array"`
		LegacyInts   []int64     `vectordata:"data"`
		LegacyFloats []float64   `vectordata:"data"`
		LegacyFlags  []bool      `vectordata:"data"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range codec.Definition().Data[3:] {
		if p.Type != vectordata.TypeJSON {
			t.Fatalf("legacy slice inference changed: %+v", p)
		}
	}
	row := vectordata.ModelRecord{Key: "a", Data: map[string]any{"Ints": []int64{1, 2}, "Floats": []float64{0.25}, "Flags": []bool{true}}}
	got, err := codec.Decode(row)
	if err != nil || !reflect.DeepEqual(got.Ints, arrayInts{1, 2}) || !reflect.DeepEqual(got.Floats, []float32{0.25}) || !reflect.DeepEqual(got.Flags, []arrayBool{true}) {
		t.Fatalf("typed arrays: %+v, %v", got, err)
	}
	row.Data["Ints"] = []int64{math.MaxInt32 + 1}
	if _, err := codec.Decode(row); err == nil {
		t.Fatal("integer array decoding overflow was truncated")
	}
	row.Data["Ints"] = []int64{1}
	row.Data["Floats"] = []float64{math.MaxFloat64}
	if _, err := codec.Decode(row); err == nil {
		t.Fatal("float array decoding overflow was truncated")
	}
	type wrong struct {
		ID     string   `vectordata:"key"`
		Values []string `vectordata:"data,type=int64_array"`
	}
	if _, err := vectordata.NewStructCodec[wrong](nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("incompatible element type: %v", err)
	}
}
