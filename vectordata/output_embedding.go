package vectordata

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
)

// Float16Bits is an IEEE binary16 bit pattern, not a numeric integer embedding.
// For example, Float16Bits(0x3c00) represents 1. Storage precision is selected
// independently by VectorProperty.Precision.
type Float16Bits uint16

// Float32 widens the bit pattern exactly, including subnormals and signed zero.
// IEEE infinities/NaNs are represented here but rejected by the output adapter.
func (h Float16Bits) Float32() float32 {
	bits := uint32(h)
	sign, exponent, fraction := (bits&0x8000)<<16, (bits>>10)&31, bits&1023
	if exponent == 0 {
		if fraction == 0 {
			return math.Float32frombits(sign)
		}
		e := int32(-14)
		for fraction&1024 == 0 {
			fraction <<= 1
			e--
		}
		return math.Float32frombits(sign | uint32(e+127)<<23 | (fraction&1023)<<13)
	}
	if exponent == 31 {
		return math.Float32frombits(sign | 0x7f800000 | fraction<<13)
	}
	return math.Float32frombits(sign | (exponent+112)<<23 | fraction<<13)
}

// EmbeddingElement describes the first supported generator representations.
// Float32 aliases are accepted. Integer outputs require an explicit Float16Bits
// representation; raw uint16, quantized integers and float64 are not inferred.
type EmbeddingElement interface{ ~float32 | Float16Bits }

// OutputEmbedder generates one typed vector per input, preserving order. Inputs
// are borrowed and must not be mutated/retained. Implementations must honor ctx
// and support concurrent use without sharing mutable output buffers between
// overlapping calls. Buffers may be reused on the next sequential call.
type OutputEmbedder[TInput any, TElement EmbeddingElement] interface {
	EmbedOutputs(context.Context, []TInput) ([][]TElement, error)
}

// OutputEmbeddingFunc adapts a function to the typed generator contract.
type OutputEmbeddingFunc[TInput any, TElement EmbeddingElement] func(context.Context, []TInput) ([][]TElement, error)

func (f OutputEmbeddingFunc[I, E]) EmbedOutputs(ctx context.Context, inputs []I) ([][]E, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil {
		return nil, fmt.Errorf("%w: nil output embedder", ErrInvalidEmbeddingOptions)
	}
	return f(ctx, inputs)
}

// AdaptOutputEmbedder adapts typed output to the existing dense InputEmbedder
// boundary. It copies float32 values or exactly widens float16 bits; existing
// property normalization separately applies dimensions, metric and storage
// precision. Configure the result in one existing InputEmbedder slot.
func AdaptOutputEmbedder[I any, E EmbeddingElement](source OutputEmbedder[I, E]) (InputEmbedder, error) {
	if isNilValue(source) {
		return nil, fmt.Errorf("%w: nil output embedder", ErrInvalidEmbeddingOptions)
	}
	return &outputEmbeddingAdapter[I, E]{source: source}, nil
}

type outputEmbeddingAdapter[I any, E EmbeddingElement] struct{ source OutputEmbedder[I, E] }

func (a *outputEmbeddingAdapter[I, E]) EmbedInputs(ctx context.Context, inputs []any) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values := make([]I, len(inputs))
	for i, input := range inputs {
		value, ok := input.(I)
		if !ok || isNilValue(input) {
			return nil, fmt.Errorf("%w: embedding input %d must be a non-nil %s, got %T", ErrInvalidEmbeddingOptions, i, reflect.TypeFor[I](), input)
		}
		values[i] = value
	}
	if len(inputs) == 0 {
		return [][]float32{}, nil
	}
	vectors, err := a.source.EmbedOutputs(ctx, values)
	if cause := ctx.Err(); cause != nil {
		return nil, errors.Join(cause, err)
	}
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(inputs) {
		return nil, fmt.Errorf("%w: expected %d output vectors, got %d", ErrInvalidVector, len(inputs), len(vectors))
	}
	var zero E
	_, half := any(zero).(Float16Bits)
	out := make([][]float32, len(vectors))
	for i, vector := range vectors {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(vector) == 0 {
			return nil, fmt.Errorf("%w: empty output vector at %d", ErrInvalidVector, i)
		}
		out[i] = make([]float32, len(vector))
		for j, value := range vector {
			x := float32(value)
			if half {
				x = Float16Bits(uint16(value)).Float32()
			}
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return nil, fmt.Errorf("%w: non-finite output at vector %d element %d", ErrInvalidVector, i, j)
			}
			out[i][j] = x
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
