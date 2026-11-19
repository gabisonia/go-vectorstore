package vectordata_test

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

var errNativeJSONDecode = errors.New("native mapping invoked JSON decoder")

type nativeDecodeInt int16
type nativeDecodeUint uint8
type nativeDecodeFloat float32
type nativeDecodeBool bool
type nativeDecodeString string
type nativeDecodeSlice []int16

func (*nativeDecodeInt) UnmarshalJSON([]byte) error    { return errNativeJSONDecode }
func (*nativeDecodeUint) UnmarshalJSON([]byte) error   { return errNativeJSONDecode }
func (*nativeDecodeFloat) UnmarshalJSON([]byte) error  { return errNativeJSONDecode }
func (*nativeDecodeBool) UnmarshalJSON([]byte) error   { return errNativeJSONDecode }
func (*nativeDecodeString) UnmarshalJSON([]byte) error { return errNativeJSONDecode }
func (*nativeDecodeSlice) UnmarshalJSON([]byte) error  { return errNativeJSONDecode }

type nativeDecodeModel[T any] struct {
	ID    string
	Value T
}

func checkNativeDecode[T any](t *testing.T, kind vectordata.DataType, source any, want T) {
	t.Helper()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Value", Type: kind}}}
	codec, err := vectordata.NewStructCodec[nativeDecodeModel[T]](&d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(vectordata.ModelRecord{Key: "a", Data: map[string]any{"Value": source}})
	if err != nil || !reflect.DeepEqual(got.Value, want) {
		t.Fatalf("native %s decoding: %+v, %v; want %v", kind, got, err, want)
	}
	// Native decoding owns its output even for identical slice types.
	if value := reflect.ValueOf(got.Value); value.Kind() == reflect.Slice && value.Len() > 0 {
		before := reflect.MakeSlice(reflect.TypeOf(source), value.Len(), value.Len())
		reflect.Copy(before, reflect.ValueOf(source))
		value.Index(0).Set(reflect.Zero(value.Type().Elem()))
		if !reflect.DeepEqual(source, before.Interface()) {
			t.Fatal("native decode aliases source slice")
		}
	}
}

func TestNativeDecodeAvoidsJSONHooks(t *testing.T) {
	t.Run("signed elements", func(t *testing.T) {
		checkNativeDecode(t, vectordata.TypeInt64Array, []int64{1, -2, math.MaxInt16}, []nativeDecodeInt{1, -2, math.MaxInt16})
	})
	t.Run("unsigned elements", func(t *testing.T) {
		checkNativeDecode(t, vectordata.TypeInt64Array, []int64{0, math.MaxUint8}, []nativeDecodeUint{0, math.MaxUint8})
	})
	t.Run("float elements", func(t *testing.T) {
		checkNativeDecode(t, vectordata.TypeFloat64Array, []float64{0.1, math.MaxFloat32}, []nativeDecodeFloat{0.1, math.MaxFloat32})
	})
	t.Run("bool elements", func(t *testing.T) {
		checkNativeDecode(t, vectordata.TypeBoolArray, []bool{true, false}, []nativeDecodeBool{true, false})
	})
	t.Run("slice decoder", func(t *testing.T) {
		checkNativeDecode(t, vectordata.TypeInt64Array, []int64{math.MaxInt16}, nativeDecodeSlice{math.MaxInt16})
	})
	t.Run("assignable slice ownership", func(t *testing.T) {
		checkNativeDecode(t, vectordata.TypeInt64Array, []int64{9007199254740993}, []int64{9007199254740993})
	})
	t.Run("string elements", func(t *testing.T) {
		checkNativeDecode(t, vectordata.TypeStringArray, []string{"hello"}, []nativeDecodeString{"hello"})
	})
	t.Run("bool scalar", func(t *testing.T) {
		checkNativeDecode(t, vectordata.TypeBool, true, nativeDecodeBool(true))
	})
	t.Run("JSON mapping retains hook", func(t *testing.T) {
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Value", Type: vectordata.TypeJSON}}}
		codec, err := vectordata.NewStructCodec[nativeDecodeModel[nativeDecodeBool]](&d)
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode(vectordata.ModelRecord{Key: "a", Data: map[string]any{"Value": true}})
		if !errors.Is(err, errNativeJSONDecode) || got.ID != "" {
			t.Fatalf("JSON decoder bypassed: %+v, %v", got, err)
		}
	})
}

func TestNativeDecodeRejectsNarrowingWithoutPartialModels(t *testing.T) {
	type model struct {
		ID     string              `vectordata:"key"`
		Ints   []nativeDecodeInt   `vectordata:"data,type=int64_array"`
		Uints  []nativeDecodeUint  `vectordata:"data,type=int64_array"`
		Floats []nativeDecodeFloat `vectordata:"data,type=float64_array"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		field string
		bad   any
	}{
		{"Ints", []int64{1, math.MaxInt16 + 1}},
		{"Ints", []int64{1, math.MinInt16 - 1}},
		{"Uints", []int64{1, -1}},
		{"Uints", []int64{1, math.MaxUint8 + 1}},
		{"Floats", []float64{1, math.MaxFloat64}},
	} {
		row := vectordata.ModelRecord{Key: "a", Data: map[string]any{"Ints": []int64{1}, "Uints": []int64{1}, "Floats": []float64{1}}}
		row.Data[tt.field] = tt.bad
		got, err := codec.Decode(row)
		if err == nil || errors.Is(err, errNativeJSONDecode) || !strings.Contains(err.Error(), tt.field) || !reflect.DeepEqual(got, model{}) {
			t.Fatalf("native %s narrowing returned partial model or invoked JSON: %+v, %v", tt.field, got, err)
		}
	}
}
