package vectordata_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestCalendarParsing(t *testing.T) {
	for _, text := range []string{"0001-01-01", "2000-02-29", "2024-02-29", "9999-12-31"} {
		got, err := vectordata.ParseDateOnly(text)
		if err != nil || got.String() != text {
			t.Fatalf("date %q: %v, %v", text, got, err)
		}
	}
	for _, text := range []string{"", "0000-01-01", "10000-01-01", "1900-02-29", "2026-02-29", "2026-04-31", "2026-1-01", " 2026-01-01", "2026-01-01Z", "2026-01-01T00:00:00Z", "infinity", "today"} {
		if _, err := vectordata.ParseDateOnly(text); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("accepted date %q: %v", text, err)
		}
	}
	for _, tt := range []struct{ input, want string }{{"00:00:00", "00:00:00.000000"}, {"09:02:03.1", "09:02:03.100000"}, {"09:02:03.123456789", "09:02:03.123456"}, {"23:59:59.999999999", "23:59:59.999999"}} {
		got, err := vectordata.ParseTimeOnly(tt.input)
		if err != nil || got.String() != tt.want {
			t.Fatalf("time %q: %v, %v", tt.input, got, err)
		}
	}
	for _, text := range []string{"", "24:00:00", "23:59:60", "12:60:00", "9:02:03", "09:02", "09:02:03.", "09:02:03,1", "09:02:03.1234567890", "09:02:03Z", "09:02:03+01:00", " 09:02:03", "now"} {
		if _, err := vectordata.ParseTimeOnly(text); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("accepted time %q: %v", text, err)
		}
	}
}

func TestCalendarNormalization(t *testing.T) {
	type dateText string
	for _, tt := range []struct {
		kind        vectordata.DataType
		input, want any
	}{
		{vectordata.TypeDateOnly, dateText("2024-02-29"), "2024-02-29"},
		{vectordata.TypeTimeOnly, vectordata.TimeOnly("09:02:03.123456789"), "09:02:03.123456"},
		{vectordata.TypeDateOnlyArray, []vectordata.DateOnly{"2024-02-29"}, []string{"2024-02-29"}},
		{vectordata.TypeTimeOnlyArray, []any{dateText("09:02:03.123456789")}, []string{"09:02:03.123456"}},
		{vectordata.TypeDateOnlyArray, []string{}, []string{}},
		{vectordata.TypeTimeOnlyArray, []string{}, []string{}},
	} {
		got, err := vectordata.NormalizeModelData(tt.input, vectordata.DataProperty{Type: tt.kind})
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%s: %v, %v", tt.kind, got, err)
		}
	}
	for _, kind := range []vectordata.DataType{vectordata.TypeDateOnly, vectordata.TypeTimeOnly} {
		for _, value := range []any{time.Now(), time.Hour, 123, ""} {
			if _, err := vectordata.NormalizeModelData(value, vectordata.DataProperty{Type: kind}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("accepted %s value %v: %v", kind, value, err)
			}
		}
	}
	for _, kind := range []vectordata.DataType{vectordata.TypeDateOnlyArray, vectordata.TypeTimeOnlyArray} {
		for _, input := range []any{nil, []string(nil)} {
			if got, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: kind, Optional: true}); err != nil || got != nil {
				t.Fatalf("optional NULL array: %v, %v", got, err)
			}
			if _, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: kind}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("accepted required NULL array: %v", err)
			}
		}
		for _, input := range []any{[]any{nil}, [][]string{{}}, []time.Time{}, []string{"invalid"}} {
			if _, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: kind}); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("accepted invalid array %v: %v", input, err)
			}
		}
	}
}

func TestCalendarTextAndJSON(t *testing.T) {
	var date vectordata.DateOnly
	var clock vectordata.TimeOnly
	if err := date.UnmarshalText([]byte("2024-02-29")); err != nil {
		t.Fatal(err)
	}
	if err := clock.UnmarshalText([]byte("09:02:03.123456789")); err != nil {
		t.Fatal(err)
	}
	if err := date.UnmarshalText([]byte("bad")); err == nil || date != "2024-02-29" {
		t.Fatal("invalid text changed the date")
	}
	if err := clock.UnmarshalText([]byte("bad")); err == nil || clock != "09:02:03.123456" {
		t.Fatal("invalid text changed the time")
	}
	encoded, err := json.Marshal([]any{date, clock})
	if err != nil || string(encoded) != `["2024-02-29","09:02:03.123456"]` {
		t.Fatalf("calendar JSON: %s, %v", encoded, err)
	}
	if _, err := json.Marshal(vectordata.DateOnly("bad")); err == nil {
		t.Fatal("invalid date marshaled")
	}
}

func TestCalendarCodecAndInference(t *testing.T) {
	type model struct {
		ID          string                `vectordata:"key"`
		Date        vectordata.DateOnly   `vectordata:"data,type=date_only"`
		Clock       *vectordata.TimeOnly  `vectordata:"data,type=time_only"`
		Dates       []vectordata.DateOnly `vectordata:"data,type=date_only_array,optional"`
		Legacy      string                `vectordata:"data"`
		LegacyArray []string              `vectordata:"data"`
		Timestamp   time.Time             `vectordata:"data"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []vectordata.DataType{vectordata.TypeDateOnly, vectordata.TypeTimeOnly, vectordata.TypeDateOnlyArray, vectordata.TypeString, vectordata.TypeStringArray, vectordata.TypeTime} {
		if got := codec.Definition().Data[i].Type; got != want {
			t.Fatalf("inferred %s, want %s", got, want)
		}
	}
	got, err := codec.Decode(vectordata.ModelRecord{Key: "a", Data: map[string]any{"Date": "2024-02-29", "Clock": "09:02:03.123456789", "Dates": []string{"0001-01-01"}}})
	if err != nil || got.Date != "2024-02-29" || got.Clock == nil || *got.Clock != "09:02:03.123456" || !reflect.DeepEqual(got.Dates, []vectordata.DateOnly{"0001-01-01"}) {
		t.Fatalf("calendar decoding: %+v, %v", got, err)
	}
	type wrong struct {
		ID   string    `vectordata:"key"`
		Date time.Time `vectordata:"data,type=date_only"`
	}
	if _, err := vectordata.NewStructCodec[wrong](nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("timestamp mapped to calendar date: %v", err)
	}
	for _, kind := range []vectordata.DataType{vectordata.TypeDateOnly, vectordata.TypeTimeOnly} {
		var null *vectordata.DateOnly
		if got, err := vectordata.NormalizeModelData(null, vectordata.DataProperty{Type: kind, Optional: true}); err != nil || got != nil {
			t.Fatalf("optional typed NULL: %v, %v", got, err)
		}
		if _, err := vectordata.NormalizeModelData(null, vectordata.DataProperty{Type: kind}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("required typed NULL accepted: %v", err)
		}
	}
}
