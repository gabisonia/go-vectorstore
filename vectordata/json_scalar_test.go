package vectordata_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type jsonInteger int64

func (v jsonInteger) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Value int64 `json:"value"`
	}{int64(v)})
}
func (v *jsonInteger) UnmarshalJSON(raw []byte) error {
	var object struct {
		Value int64 `json:"value"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	*v = jsonInteger(object.Value)
	return nil
}

type jsonBoolean bool

func (v jsonBoolean) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Value bool `json:"value"`
	}{bool(v)})
}

type jsonText string

func (v jsonText) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Value string `json:"value"`
	}{string(v)})
}

type pointerJSONInteger int64

func (v *pointerJSONInteger) MarshalJSON() ([]byte, error) { return jsonInteger(*v).MarshalJSON() }

var errScalarMarshaling = errors.New("scalar JSON marshaling failed")

type invalidJSONScalar int

func (invalidJSONScalar) MarshalJSON() ([]byte, error) { return nil, errScalarMarshaling }

func TestJSONScalarEncodingRetainsNumberAndFloat32Representation(t *testing.T) {
	type model struct {
		ID     string      `vectordata:"key"`
		Number json.Number `vectordata:"data,type=json"`
		Float  float32     `vectordata:"data,type=json"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := codec.Encode(model{ID: "a", Number: json.Number("9007199254740993"), Float: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := vectordata.PrepareModelRecords(t.Context(), codec.Definition(), vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{r})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0].Data["Number"]; got != json.Number("9007199254740993") {
		t.Fatalf("JSON number lost its representation: %#v (%T)", got, got)
	}
	if got := rows[0].Data["Float"]; got != json.Number("0.1") {
		t.Fatalf("JSON float32 widened before serialization: %#v", got)
	}
	r, err = codec.Encode(model{ID: "invalid", Number: json.Number("not-a-number")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vectordata.PrepareModelRecords(t.Context(), codec.Definition(), vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{r}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("invalid JSON number accepted as text: %v", err)
	}
}

func TestJSONScalarEncodingUsesDeclaredRepresentation(t *testing.T) {
	type model struct {
		ID            string              `vectordata:"key"`
		Integer       jsonInteger         `vectordata:"data,type=json"`
		Boolean       jsonBoolean         `vectordata:"data,type=json"`
		Text          jsonText            `vectordata:"data,type=json"`
		Pointer       *pointerJSONInteger `vectordata:"data,type=json"`
		NativeInteger jsonInteger         `vectordata:"data"`
		NativeBoolean jsonBoolean         `vectordata:"data"`
		NativeText    jsonText            `vectordata:"data"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	value := pointerJSONInteger(9)
	r, err := codec.Encode(model{ID: "a", Integer: 7, Boolean: true, Text: "literal", Pointer: &value, NativeInteger: 3, NativeBoolean: true, NativeText: "native"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := vectordata.PrepareModelRecords(t.Context(), codec.Definition(), vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{r})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"Integer": map[string]any{"value": json.Number("7")}, "Boolean": map[string]any{"value": true}, "Text": map[string]any{"value": "literal"}, "Pointer": map[string]any{"value": json.Number("9")}, "NativeInteger": int64(3), "NativeBoolean": true, "NativeText": "native"}
	if !reflect.DeepEqual(prepared[0].Data, want) {
		t.Fatalf("declared representations changed: %#v", prepared[0].Data)
	}
	r, err = codec.Encode(model{ID: "nil"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Data["Pointer"] != nil {
		t.Fatalf("nil JSON pointer lost SQL NULL semantics: %#v", r.Data["Pointer"])
	}
}

func TestExplicitJSONScalarMarshalerFailureIsPreserved(t *testing.T) {
	type model struct {
		ID    string            `vectordata:"key"`
		Value invalidJSONScalar `vectordata:"data,type=json"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := codec.Encode(model{ID: "a", Value: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = vectordata.PrepareModelRecords(t.Context(), codec.Definition(), vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{r})
	var marshaler *json.MarshalerError
	if !errors.Is(err, vectordata.ErrInvalidRecord) || !errors.Is(err, errScalarMarshaling) || !errors.As(err, &marshaler) {
		t.Fatalf("scalar marshaler was bypassed/lost: %v", err)
	}
}
