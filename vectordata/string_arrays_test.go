package vectordata_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type stringLabel string
type stringLabels []stringLabel

func TestNamedStringArrayCodecNormalization(t *testing.T) {
	type model struct {
		ID   string       `vectordata:"key"`
		Tags stringLabels `vectordata:"data"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	definition := codec.Definition()
	if definition.Data[0].Type != vectordata.TypeStringArray {
		t.Fatalf("inference: %+v", definition)
	}
	input := model{ID: "one", Tags: stringLabels{"go", "guide"}}
	record, err := codec.Encode(input)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := vectordata.PrepareModelRecords(t.Context(), definition, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{record})
	if err != nil {
		t.Fatal(err)
	}
	normalized := prepared[0]
	if !reflect.DeepEqual(normalized.Data["Tags"], []string{"go", "guide"}) {
		t.Fatalf("canonical value: %#v", normalized.Data["Tags"])
	}
	decoded, err := codec.Decode(normalized)
	if err != nil || !reflect.DeepEqual(decoded, input) {
		t.Fatalf("typed round trip: %+v, %v", decoded, err)
	}
}

func TestStringArrayNormalizationKindsAndOwnership(t *testing.T) {
	type namedSlice []string
	for _, input := range []any{[]string{"a", "b"}, namedSlice{"a", "b"}, []stringLabel{"a", "b"}, stringLabels{"a", "b"}} {
		value, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeStringArray})
		if err != nil || !reflect.DeepEqual(value, []string{"a", "b"}) {
			t.Fatalf("normalize %T: %#v, %v", input, value, err)
		}
		reflect.ValueOf(input).Index(0).SetString("changed")
		if value.([]string)[0] != "a" {
			t.Fatalf("normalized %T aliases its input", input)
		}
	}
	for _, input := range []any{[]string(nil), namedSlice(nil), []stringLabel(nil), stringLabels(nil), stringLabels{}} {
		for _, optional := range []bool{false, true} {
			value, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeStringArray, Optional: optional})
			if err != nil || !reflect.DeepEqual(value, []string{}) {
				t.Fatalf("legacy empty/null %T: %#v, %v", input, value, err)
			}
		}
	}
	for _, input := range []any{[1]string{"a"}, []any{"a"}, []int{}, "a"} {
		if _, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeStringArray}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("accepted %T: %v", input, err)
		}
	}
	if value, err := vectordata.NormalizeModelData(nil, vectordata.DataProperty{Type: vectordata.TypeStringArray, Optional: true}); err != nil || value != nil {
		t.Fatalf("untyped optional NULL: %v, %v", value, err)
	}
}
