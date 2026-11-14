package vectordata_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func halfValue(bits int) float32 {
	exponent, mantissa := (bits>>10)&31, bits&1023
	if exponent == 0 {
		return float32(math.Ldexp(float64(mantissa), -24))
	}
	return float32(math.Ldexp(1+float64(mantissa)/1024, exponent-15))
}

func TestFloat16Rounding(t *testing.T) {
	p := vectordata.VectorProperty{Name: "Vector", Dimensions: 1, Precision: vectordata.VectorFloat16, Metric: vectordata.DistanceL2}
	check := func(input, want float32) {
		t.Helper()
		out, err := vectordata.NormalizeModelVector([]float32{input}, p)
		if err != nil || math.Float32bits(out[0]) != math.Float32bits(want) {
			t.Fatalf("round(%g)=%v, want %g, error=%v", input, out, want, err)
		}
	}
	for bits := 0; bits < 0x7c00; bits++ {
		value := halfValue(bits)
		check(value, value)
		check(-value, -value)
		if bits == 0x7bff {
			continue
		}
		next := halfValue(bits + 1)
		midpoint := (value + next) / 2
		want := value
		if bits&1 != 0 {
			want = next
		}
		check(midpoint, want)
		check(-midpoint, -want)
		check(math.Nextafter32(midpoint, float32(math.Inf(-1))), value)
		check(math.Nextafter32(midpoint, float32(math.Inf(1))), next)
	}
	check(65519, 65504)
	check(-65519, -65504)
	for _, value := range []float32{65520, -65520, math.MaxFloat32, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		if _, err := vectordata.NormalizeModelVector([]float32{value}, p); !errors.Is(err, vectordata.ErrInvalidVector) {
			t.Fatalf("accepted invalid float16 value %g: %v", value, err)
		}
	}
	p.Metric = vectordata.DistanceCosine
	if err := vectordata.ValidateModelVector([]float32{math.SmallestNonzeroFloat32}, p); !errors.Is(err, vectordata.ErrInvalidVector) {
		t.Fatalf("cosine rounded to zero: %v", err)
	}
}

func TestVectorPrecisionDefinitionsAndCompatibility(t *testing.T) {
	type halfModel struct {
		ID     string    `vectordata:"key"`
		Vector []float32 `vectordata:"vector,dimensions=3,precision=float16"`
	}
	d, err := vectordata.DefinitionFor[halfModel]()
	if err != nil || d.Vectors[0].Precision != vectordata.VectorFloat16 {
		t.Fatalf("tagged definition: %+v, %v", d, err)
	}
	d.Vectors[0].Precision = vectordata.VectorFloat32
	defaulted, err := d.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(defaulted)
	if err != nil || strings.Contains(string(raw), "Precision") {
		t.Fatalf("float32 registration changed: %s, %v", raw, err)
	}
	defaulted.Vectors[0].Precision = "unknown"
	if _, err := defaulted.Normalize(); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("unknown precision: %v", err)
	}
	input := []float32{0.1, 0.2, 0.3}
	if _, err := vectordata.NormalizeModelVector(input, d.Vectors[0]); err != nil {
		t.Fatal(err)
	}
	if input[0] != float32(0.1) {
		t.Fatal("normalization changed input")
	}
	type invalidTag struct {
		ID string `vectordata:"key,precision=float16"`
	}
	if _, err := vectordata.DefinitionFor[invalidTag](); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("precision on key: %v", err)
	}
}
