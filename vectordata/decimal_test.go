package vectordata_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestParseDecimal(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"+001.2300e+2", "123"}, {"-0.000e100", "0"}, {".1", "0.1"}, {"1.", "1"}, {"-12.3400", "-12.34"},
		{"9007199254740993.000000001", "9007199254740993.000000001"},
		{"79228162514264337593543950335", "79228162514264337593543950335"},
		{"-79228162514264337593543950335.0", "-79228162514264337593543950335"},
		{"7.9228162514264337593543950335", "7.9228162514264337593543950335"},
		{"1e-28", "0.0000000000000000000000000001"}, {"10e-29", "0.0000000000000000000000000001"},
		{"1.00000000000000000000000000000", "1"}, {"1e28", "10000000000000000000000000000"},
	} {
		got, err := vectordata.ParseDecimal(tt.input)
		if err != nil || got.String() != tt.want {
			t.Fatalf("decimal %q: %q, %v", tt.input, got, err)
		}
		again, err := vectordata.ParseDecimal(got.String())
		if err != nil || again != got {
			t.Fatalf("decimal canonicalization not stable: %v, %v", again, err)
		}
	}
	for _, text := range []string{"", " ", " 1", "1 ", "NaN", "Infinity", "-Infinity", "1/3", "1_000", ".", "+", "1e", "1e+", "1e2147483648", "1e-2147483649", "1e1000000000", "1e-1000000000", "1e29", "1e-29", "79228162514264337593543950336", "7.9228162514264337593543950336", "-79228162514264337593543950336", strings.Repeat("9", 1000)} {
		if _, err := vectordata.ParseDecimal(text); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("accepted decimal %q: %v", text, err)
		}
	}
}

func TestDecimalScaleBoundariesPreserveValue(t *testing.T) {
	for scale := 0; scale <= 28; scale++ {
		for _, coefficient := range []string{"1", "12345678901234567890123456789", "79228162514264337593543950335"} {
			for _, sign := range []string{"", "-"} {
				input := fmt.Sprintf("%s%se-%d", sign, coefficient, scale)
				value, err := vectordata.ParseDecimal(input)
				if err != nil {
					t.Fatal(err)
				}
				want, ok := new(big.Rat).SetString(input)
				if !ok {
					t.Fatal("invalid rational test input")
				}
				got, ok := new(big.Rat).SetString(value.String())
				if !ok || got.Cmp(want) != 0 {
					t.Fatalf("decimal rounded: %s became %s", input, value)
				}
			}
		}
	}
}

func TestDecimalNormalizationAndJSON(t *testing.T) {
	type decimalText string
	for _, input := range []any{vectordata.Decimal("12.300"), json.Number("123e-1"), decimalText("012.30"), "+12.3"} {
		got, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeDecimal})
		if err != nil || got != vectordata.Decimal("12.3") {
			t.Fatalf("decimal normalization: %v, %v", got, err)
		}
	}
	for _, input := range []any{int64(math.MaxInt64), uint64(math.MaxUint64)} {
		if _, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeDecimal}); err != nil {
			t.Fatalf("integer decimal: %v", err)
		}
	}
	for _, input := range []any{0.1, float32(1), math.NaN(), math.Inf(1), true, []byte("1")} {
		if _, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeDecimal}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("accepted inexact decimal %v: %v", input, err)
		}
	}
	value := vectordata.Decimal("9007199254740993.000000001")
	data, err := json.Marshal(value)
	if err != nil || string(data) != value.String() {
		t.Fatalf("decimal JSON: %s, %v", data, err)
	}
	var decoded vectordata.Decimal
	if err := json.Unmarshal(data, &decoded); err != nil || decoded != value {
		t.Fatalf("decimal JSON decode: %v, %v", decoded, err)
	}
	for _, data := range []string{`"12.3"`, ` "12.3" `, `null`, `true`, `[1]`, `1e-29`} {
		if err := json.Unmarshal([]byte(data), &decoded); err == nil || decoded != value {
			t.Fatalf("invalid JSON changed decimal: %v, %v", decoded, err)
		}
	}
	if _, err := json.Marshal(vectordata.Decimal("NaN")); err == nil {
		t.Fatal("invalid decimal marshaled")
	}
	var text vectordata.Decimal
	if err := text.UnmarshalText([]byte("001.20")); err != nil || text != "1.2" {
		t.Fatalf("decimal text: %v, %v", text, err)
	}
	data, err = text.MarshalText()
	if err != nil || string(data) != "1.2" {
		t.Fatalf("decimal marshal text: %s, %v", data, err)
	}
}

func TestDecimalArraysAndCodec(t *testing.T) {
	type prices []vectordata.Decimal
	type model struct {
		ID            string              `vectordata:"key"`
		Price         vectordata.Decimal  `vectordata:"data,type=decimal,index"`
		Backup        *vectordata.Decimal `vectordata:"data,type=decimal"`
		Prices        prices              `vectordata:"data,type=decimal_array,optional,index"`
		Text          []string            `vectordata:"data,type=decimal_array,optional"`
		Legacy        string              `vectordata:"data"`
		LegacyNumbers []float64           `vectordata:"data"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	if codec.Definition().Data[4].Type != vectordata.TypeString || codec.Definition().Data[5].Type != vectordata.TypeJSON {
		t.Fatal("legacy inference changed")
	}
	got, err := codec.Decode(vectordata.ModelRecord{Key: "a", Data: map[string]any{"Price": vectordata.Decimal("12.3"), "Prices": []vectordata.Decimal{"0.1", "2"}, "Text": []vectordata.Decimal{"0.1"}}})
	if err != nil || got.Price != "12.3" || got.Backup != nil || !reflect.DeepEqual(got.Prices, prices{"0.1", "2"}) || !reflect.DeepEqual(got.Text, []string{"0.1"}) {
		t.Fatalf("typed decimals: %+v, %v", got, err)
	}
	for _, input := range []any{prices{"0.1", "2.00"}, []string{".1", "2e0"}, []any{json.Number("0.1"), 2}} {
		got, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeDecimalArray})
		if err != nil || !reflect.DeepEqual(got, []vectordata.Decimal{"0.1", "2"}) {
			t.Fatalf("decimal array: %v, %v", got, err)
		}
	}
	for _, input := range []any{nil, []vectordata.Decimal(nil), []string(nil)} {
		if got, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeDecimalArray, Optional: true}); err != nil || got != nil {
			t.Fatalf("optional NULL: %v, %v", got, err)
		}
		if _, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeDecimalArray}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("required NULL accepted: %v", err)
		}
	}
	for _, input := range []any{[]any{nil}, []float64{}, []any{0.1}, []string{"1e-29"}} {
		if _, err := vectordata.NormalizeModelData(input, vectordata.DataProperty{Type: vectordata.TypeDecimalArray}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("invalid array accepted: %v", err)
		}
	}
	type wrong struct {
		ID    string  `vectordata:"key"`
		Price float64 `vectordata:"data,type=decimal"`
	}
	if _, err := vectordata.NewStructCodec[wrong](nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("float decimal mapping accepted: %v", err)
	}
	if got := vectordata.CompareModelValues(vectordata.Decimal("2"), vectordata.Decimal("10")); got >= 0 {
		t.Fatal("decimals sorted lexicographically")
	}
}
