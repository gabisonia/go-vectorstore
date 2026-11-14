package vectordata

import (
	"fmt"
	"math"
)

// VectorPrecision selects stored vector precision; Go inputs/outputs stay float32.
type VectorPrecision string

const (
	VectorFloat32 VectorPrecision = "float32"
	VectorFloat16 VectorPrecision = "float16"
)

// roundFloat16 rounds a float32 to IEEE binary16 using nearest, ties to even,
// returning the result as float32. Subnormals use the binary16 spacing of 2^-24.
func roundFloat16(x float32) float32 {
	bits := math.Float32bits(x)
	abs := bits & 0x7fffffff
	if abs < 0x38800000 {
		return float32(math.Ldexp(math.RoundToEven(math.Ldexp(float64(x), 24)), -24))
	}
	if abs >= 0x47800000 { // 2^16 and above cannot be finite binary16.
		return math.Float32frombits(bits&0x80000000 | 0x7f800000)
	}
	abs = (abs + 0xfff + (abs>>13)&1) &^ 0x1fff
	if abs >= 0x47800000 {
		abs = 0x7f800000
	}
	return math.Float32frombits(bits&0x80000000 | abs)
}

// NormalizeModelVector validates storage precision and vector values. Float16
// produces a new slice rounded to binary16; float32 returns the input unchanged.
// Providers must copy float32 inputs before retaining them.
func NormalizeModelVector(vector []float32, p VectorProperty) ([]float32, error) {
	return modelVector(vector, p, true)
}

func modelVector(vector []float32, p VectorProperty, normalize bool) ([]float32, error) {
	if p.Precision != "" && p.Precision != VectorFloat32 && p.Precision != VectorFloat16 {
		return nil, fmt.Errorf("%w: unsupported vector precision %q", ErrSchemaMismatch, p.Precision)
	}
	if len(vector) != p.Dimensions {
		return nil, fmt.Errorf("%w: property %q expects %d, got %d", ErrDimensionMismatch, p.Name, p.Dimensions, len(vector))
	}
	out := vector
	if normalize && p.Precision == VectorFloat16 {
		out = make([]float32, len(vector))
	}
	nonzero := false
	for i, x := range vector {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, fmt.Errorf("%w: non-finite element in %q", ErrInvalidVector, p.Name)
		}
		if p.Precision == VectorFloat16 {
			x = roundFloat16(x)
			if math.IsInf(float64(x), 0) || math.Abs(float64(x)) > 65504 {
				return nil, fmt.Errorf("%w: float16 overflow in %q", ErrInvalidVector, p.Name)
			}
			if normalize {
				out[i] = x
			}
		}
		nonzero = nonzero || x != 0
	}
	if !nonzero && (p.Metric == "" || p.Metric == DistanceCosine) {
		return nil, fmt.Errorf("%w: zero cosine vector after precision conversion in %q", ErrInvalidVector, p.Name)
	}
	return out, nil
}
