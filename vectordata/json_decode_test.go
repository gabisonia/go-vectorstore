package vectordata_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

var errJSONToken = errors.New("invalid encoded token")

type jsonToken string

func (v jsonToken) MarshalJSON() ([]byte, error) { return json.Marshal("wire:" + string(v)) }
func (v *jsonToken) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	if !strings.HasPrefix(text, "wire:") {
		return errJSONToken
	}
	*v = jsonToken(strings.TrimPrefix(text, "wire:"))
	return nil
}
func (v *jsonToken) UnmarshalText(raw []byte) error {
	*v = jsonToken("text:" + string(raw))
	return nil
}

func TestJSONDecodeUsesJSONUnmarshalerBeforeNativeAssignments(t *testing.T) {
	type model struct {
		ID    string    `vectordata:"key"`
		Token jsonToken `vectordata:"data,type=json"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(vectordata.ModelRecord{Key: "a", Data: map[string]any{"Token": "wire:hello"}})
	if err != nil || got.Token != "hello" {
		t.Fatalf("JSON hook bypassed: %+v, %v", got, err)
	}
	got, err = codec.Decode(vectordata.ModelRecord{Key: "a", Data: map[string]any{"Token": "invalid"}})
	if !errors.Is(err, errJSONToken) || got != (model{}) {
		t.Fatalf("JSON validation hook bypassed: %+v, %v", got, err)
	}
}

type validatedJSONMap map[string]any

func (v *validatedJSONMap) UnmarshalJSON(raw []byte) error {
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoded["ok"] != true {
		return errJSONToken
	}
	*v = decoded
	return nil
}

type validatedJSONSlice []any

func (v *validatedJSONSlice) UnmarshalJSON(raw []byte) error {
	var decoded []any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if len(decoded) == 0 {
		return errJSONToken
	}
	*v = decoded
	return nil
}

func TestJSONDecodeMapSliceHooksValidateAndCopy(t *testing.T) {
	type model struct {
		ID     string             `vectordata:"key"`
		Object validatedJSONMap   `vectordata:"data,type=json"`
		Array  validatedJSONSlice `vectordata:"data,type=json"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	object := map[string]any{"ok": true, "number": json.Number("9007199254740993")}
	array := []any{json.Number("9007199254740993")}
	record := vectordata.ModelRecord{Key: "a", Data: map[string]any{"Object": object, "Array": array}}
	got, err := codec.Decode(record)
	if err != nil {
		t.Fatal(err)
	}
	if got.Object["number"] != json.Number("9007199254740993") || got.Array[0] != json.Number("9007199254740993") {
		t.Fatalf("custom JSON number precision: %+v", got)
	}
	got.Object["number"] = "changed"
	got.Array[0] = "changed"
	if object["number"] != json.Number("9007199254740993") || array[0] != json.Number("9007199254740993") {
		t.Fatal("custom JSON decode aliases source buffers")
	}
	for _, field := range []string{"Object", "Array"} {
		bad := map[string]any{"Object": object, "Array": array}
		if field == "Object" {
			bad[field] = map[string]any{"ok": false}
		} else {
			bad[field] = []any{}
		}
		got, err := codec.Decode(vectordata.ModelRecord{Key: "a", Data: bad})
		if !errors.Is(err, errJSONToken) || !reflect.DeepEqual(got, model{}) || !strings.Contains(err.Error(), field) {
			t.Fatalf("JSON %s hook error lost/partial model returned: %+v, %v", field, got, err)
		}
	}
}

type nullJSONValue struct{ seen, null bool }

func (v *nullJSONValue) UnmarshalJSON(raw []byte) error {
	v.seen = true
	v.null = string(raw) == "null"
	return nil
}

func TestJSONDecodeHooksPreserveNullAndNativeMappings(t *testing.T) {
	type model struct {
		ID      string          `vectordata:"key"`
		Value   **nullJSONValue `vectordata:"data,type=json"`
		Dynamic any             `vectordata:"data,type=json,optional"`
		Native  jsonInteger     `vectordata:"data"`
		Text    jsonToken       `vectordata:"data"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{nil, vectordata.JSONNull{}, map[string]any{}} {
		got, err := codec.Decode(vectordata.ModelRecord{Key: "a", Data: map[string]any{"Value": value, "Dynamic": vectordata.JSONNull{}, "Native": int64(3), "Text": "hello"}})
		if err != nil {
			t.Fatal(err)
		}
		if value == nil {
			if got.Value != nil {
				t.Fatal("SQL NULL allocated a JSON pointer")
			}
		} else if got.Value == nil || *got.Value == nil || !(**got.Value).seen {
			t.Fatalf("non-NULL JSON did not reach hook: %+v", got.Value)
		}
		if _, null := value.(vectordata.JSONNull); null && !(**got.Value).null {
			t.Fatal("JSON null did not reach hook as null")
		}
		if _, null := got.Dynamic.(vectordata.JSONNull); !null {
			t.Fatalf("dynamic JSON null marker changed: %#v", got.Dynamic)
		}
		if got.Native != 3 || got.Text != "text:hello" {
			t.Fatalf("native decoder mappings changed: %+v", got)
		}
	}
}
