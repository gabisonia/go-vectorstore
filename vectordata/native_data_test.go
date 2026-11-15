package vectordata_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type nativeOctet uint8
type nativeBlob []nativeOctet

func TestNativeDataNormalization(t *testing.T) {
	for _, optional := range []bool{false, true} {
		p := vectordata.DataProperty{Name: "Payload", Type: vectordata.TypeBytes, Optional: optional}
		for _, value := range []any{nil, []byte(nil), nativeBlob(nil)} {
			got, err := vectordata.NormalizeModelData(value, p)
			if optional {
				if err != nil || got != nil {
					t.Fatalf("optional null bytes: %v, %v", got, err)
				}
			} else if !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("required null bytes: %v", err)
			}
		}
	}
	input := nativeBlob{0, 1, 255}
	got, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Name: "Payload", Type: vectordata.TypeBytes})
	if err != nil || !reflect.DeepEqual(got, []byte{0, 1, 255}) {
		t.Fatalf("named byte elements: %v, %v", got, err)
	}
	input[0] = 99
	if got.([]byte)[0] != 0 {
		t.Fatal("normalized bytes alias input")
	}
	for _, invalid := range []any{"AAH/", []int{0, 1}, [2]byte{0, 1}, 1, map[string]any{}} {
		if _, err := vectordata.NormalizeModelData(invalid, vectordata.DataProperty{Name: "Payload", Type: vectordata.TypeBytes}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("accepted invalid bytes %T: %v", invalid, err)
		}
	}
	uuid, err := vectordata.NormalizeModelData("8F516DB6-38CA-4E8F-AD79-D71B015F5419", vectordata.DataProperty{Name: "Owner", Type: vectordata.TypeUUID})
	if err != nil || uuid != "8f516db6-38ca-4e8f-ad79-d71b015f5419" {
		t.Fatalf("UUID normalization: %v, %v", uuid, err)
	}
	if _, err := vectordata.NormalizeModelData("bad", vectordata.DataProperty{Name: "Owner", Type: vectordata.TypeUUID}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("invalid UUID: %v", err)
	}
}

func TestNativeDataCodecValidationAndLegacyTypes(t *testing.T) {
	type legacy struct {
		ID    string `vectordata:"key"`
		Bytes []byte `vectordata:"data"`
		UUID  string `vectordata:"data"`
	}
	d, err := vectordata.DefinitionFor[legacy]()
	if err != nil || d.Data[0].Type != vectordata.TypeJSON || d.Data[1].Type != vectordata.TypeString {
		t.Fatalf("legacy inference changed: %+v, %v", d, err)
	}
	type wrongUUID struct {
		ID    string `vectordata:"key"`
		Value int    `vectordata:"data,type=uuid"`
	}
	if _, err := vectordata.NewStructCodec[wrongUUID](nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("invalid UUID field: %v", err)
	}
	type wrongBytes struct {
		ID    string `vectordata:"key"`
		Value string `vectordata:"data,type=bytes"`
	}
	if _, err := vectordata.NewStructCodec[wrongBytes](nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("invalid byte field: %v", err)
	}
	type aliasModel struct {
		ID    string     `vectordata:"key"`
		Value nativeBlob `vectordata:"data,type=bytes"`
	}
	codec, err := vectordata.NewStructCodec[aliasModel](nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(vectordata.ModelRecord{Key: "a", Data: map[string]any{"Value": []byte{0, 1, 255}}})
	if err != nil || !reflect.DeepEqual(decoded.Value, nativeBlob{0, 1, 255}) {
		t.Fatalf("named byte elements decoding: %+v, %v", decoded, err)
	}
}
