package vectordata_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type definedInstant time.Time
type structuredUUID struct{ Value string }

func (u structuredUUID) MarshalText() ([]byte, error)     { return []byte(u.Value), nil }
func (u *structuredUUID) UnmarshalText(text []byte) error { u.Value = string(text); return nil }

func TestStructuredArrayNormalization(t *testing.T) {
	input := time.Date(2026, 7, 1, 9, 2, 3, 123456789, time.FixedZone("offset", 7200))
	want := time.Date(2026, 7, 1, 7, 2, 3, 123456000, time.UTC)
	for _, value := range []any{input, definedInstant(input), input.In(time.FixedZone("other", -5*3600))} {
		got, err := vectordata.NormalizeModelArrayElement(value, vectordata.TypeTimeArray)
		if err != nil || got != want {
			t.Fatalf("timestamp normalization: %v, %v", got, err)
		}
	}
	beforeEpoch := time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC)
	got, err := vectordata.NormalizeModelArrayElement(beforeEpoch, vectordata.TypeTimeArray)
	if err != nil || got != time.Date(1969, 12, 31, 23, 59, 59, 999999000, time.UTC) {
		t.Fatalf("pre-epoch truncation: %v, %v", got, err)
	}
	for _, value := range []time.Time{time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)} {
		if _, err := vectordata.NormalizeModelArrayElement(value, vectordata.TypeTimeArray); err != nil {
			t.Fatalf("supported boundary: %v", err)
		}
	}
	for _, value := range []any{"2026-01-01T00:00:00Z", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(1, 1, 1, 0, 0, 0, 0, time.FixedZone("outside", 3600))} {
		if _, err := vectordata.NormalizeModelArrayElement(value, vectordata.TypeTimeArray); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("accepted invalid timestamp %v: %v", value, err)
		}
	}
	const upper = "8F516DB6-38CA-4E8F-AD79-D71B015F5419"
	const canonical = "8f516db6-38ca-4e8f-ad79-d71b015f5419"
	uuids, err := vectordata.NormalizeModelData([]structuredUUID{{upper}}, vectordata.DataProperty{Type: vectordata.TypeUUIDArray})
	if err != nil || !reflect.DeepEqual(uuids, []string{canonical}) {
		t.Fatalf("UUID array: %v, %v", uuids, err)
	}
	dates, err := vectordata.NormalizeModelData([]definedInstant{definedInstant(input)}, vectordata.DataProperty{Type: vectordata.TypeTimeArray})
	if err != nil || !reflect.DeepEqual(dates, []time.Time{want}) {
		t.Fatalf("timestamp array: %v, %v", dates, err)
	}
	for _, kind := range []vectordata.DataType{vectordata.TypeUUIDArray, vectordata.TypeTimeArray} {
		if _, err := vectordata.NormalizeModelData([]any{nil}, vectordata.DataProperty{Type: kind}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("NULL array element: %v", err)
		}
	}
}

func TestStructuredArrayTypedCodecAndLegacy(t *testing.T) {
	type model struct {
		ID     string           `vectordata:"key"`
		UUIDs  []structuredUUID `vectordata:"data,type=uuid_array,optional"`
		Times  []definedInstant `vectordata:"data,type=time_array,optional"`
		Legacy []time.Time      `vectordata:"data"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	if codec.Definition().Data[2].Type != vectordata.TypeJSON {
		t.Fatal("legacy timestamp slice inference changed")
	}
	instant := time.Date(2026, 1, 1, 0, 0, 0, 999, time.FixedZone("UTC+1", 3600))
	row := vectordata.ModelRecord{Key: "a", Data: map[string]any{"UUIDs": []string{"8F516DB6-38CA-4E8F-AD79-D71B015F5419"}, "Times": []time.Time{instant}}}
	got, err := codec.Decode(row)
	if err != nil || got.UUIDs[0].Value != "8f516db6-38ca-4e8f-ad79-d71b015f5419" || time.Time(got.Times[0]) != instant.UTC().Truncate(time.Microsecond) {
		t.Fatalf("structured decoding: %+v, %v", got, err)
	}
	row.Data["UUIDs"] = []any{nil}
	if _, err := codec.Decode(row); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("codec accepted null member: %v", err)
	}
	type wrong struct {
		ID    string   `vectordata:"key"`
		Times []string `vectordata:"data,type=time_array"`
	}
	if _, err := vectordata.NewStructCodec[wrong](nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("wrong timestamp field type: %v", err)
	}
}
