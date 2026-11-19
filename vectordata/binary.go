package vectordata

import (
	"context"
	"fmt"
	"iter"
	"math/bits"
)

// VectorKind selects a vector representation. The empty default is dense,
// preserving existing model registrations.
type VectorKind string

const (
	VectorDense  VectorKind = "dense"
	VectorBinary VectorKind = "binary"
)

// BinaryVector holds packed bits, most significant bit first. Dimensions is the
// number of meaningful bits; unused low bits in the last byte must be zero.
type BinaryVector struct {
	Bytes      []byte
	Dimensions int
}

func validateBinary(v BinaryVector) error {
	if v.Dimensions < 1 || v.Dimensions > 64000 {
		return fmt.Errorf("%w: binary dimensions must be 1–64000", ErrDimensionMismatch)
	}
	if len(v.Bytes) != (v.Dimensions+7)/8 {
		return fmt.Errorf("%w: binary byte length does not match dimensions", ErrDimensionMismatch)
	}
	if remainder := v.Dimensions % 8; remainder != 0 && v.Bytes[len(v.Bytes)-1]&byte((1<<(8-remainder))-1) != 0 {
		return fmt.Errorf("%w: unused binary padding bits must be zero", ErrInvalidVector)
	}
	return nil
}

// NewBinaryVector validates and copies packed bytes.
func NewBinaryVector(data []byte, dimensions int) (BinaryVector, error) {
	v := BinaryVector{Bytes: data, Dimensions: dimensions}
	if err := validateBinary(v); err != nil {
		return BinaryVector{}, err
	}
	v.Bytes = append([]byte(nil), data...)
	return v, nil
}

// ParseBinaryVector accepts a nonempty string of 0/1 bits, including bit lengths
// that are not a multiple of eight.
func ParseBinaryVector(text string) (BinaryVector, error) {
	if len(text) < 1 || len(text) > 64000 {
		return BinaryVector{}, fmt.Errorf("%w: binary dimensions must be 1–64000", ErrDimensionMismatch)
	}
	v := BinaryVector{Bytes: make([]byte, (len(text)+7)/8), Dimensions: len(text)}
	for i, c := range []byte(text) {
		switch c {
		case '1':
			v.Bytes[i/8] |= 1 << uint(7-i%8)
		case '0':
		default:
			return BinaryVector{}, fmt.Errorf("%w: binary vectors require 0/1 bits", ErrInvalidVector)
		}
	}
	return v, nil
}

// BitString returns the exact bits, without padding, for a valid binary vector.
func (v BinaryVector) BitString() (string, error) {
	if err := validateBinary(v); err != nil {
		return "", err
	}
	text := make([]byte, v.Dimensions)
	for i := range text {
		text[i] = '0' + ((v.Bytes[i/8] >> uint(7-i%8)) & 1)
	}
	return string(text), nil
}

// NormalizeBinaryVector validates a binary property and returns an owned copy.
func NormalizeBinaryVector(v BinaryVector, p VectorProperty) (BinaryVector, error) {
	if p.Kind != VectorBinary {
		return BinaryVector{}, fmt.Errorf("%w: property %q is not binary", ErrInvalidVector, p.Name)
	}
	if v.Dimensions != p.Dimensions {
		return BinaryVector{}, fmt.Errorf("%w: property %q expects %d bits, got %d", ErrDimensionMismatch, p.Name, p.Dimensions, v.Dimensions)
	}
	return NewBinaryVector(v.Bytes, v.Dimensions)
}

// HammingDistance counts differing bits without floating-point conversion.
func HammingDistance(a, b BinaryVector) (int, error) {
	if err := validateBinary(a); err != nil {
		return 0, err
	}
	if err := validateBinary(b); err != nil {
		return 0, err
	}
	if a.Dimensions != b.Dimensions {
		return 0, ErrDimensionMismatch
	}
	n := 0
	for i, x := range a.Bytes {
		n += bits.OnesCount8(x ^ b.Bytes[i])
	}
	return n, nil
}

// BinarySearcher is optional, preserving existing ModelCollection providers.
type BinarySearcher interface {
	SearchBinary(context.Context, BinaryVector, int, ModelSearchOptions) ([]ModelSearchResult, error)
	SearchBinaryIter(context.Context, BinaryVector, int, ModelSearchOptions) iter.Seq2[ModelSearchResult, error]
}

// BinaryHybridSearcher optionally combines binary similarity and keyword search.
type BinaryHybridSearcher interface {
	HybridSearchBinary(context.Context, BinaryVector, string, int, HybridSearchOptions) ([]ModelSearchResult, error)
}

func (c *TypedModelCollection[K, T]) SearchBinary(ctx context.Context, v BinaryVector, top int, opts ModelSearchOptions) ([]TypedModelSearchResult[T], error) {
	b, ok := c.base.(BinarySearcher)
	if !ok {
		return nil, ErrUnsupported
	}
	return c.searchResults(ctx, func() ([]ModelSearchResult, error) { return b.SearchBinary(ctx, v, top, opts) })
}
func (c *TypedModelCollection[K, T]) SearchBinaryIter(ctx context.Context, v BinaryVector, top int, opts ModelSearchOptions) iter.Seq2[TypedModelSearchResult[T], error] {
	return c.searchSequence(ctx, func(yield func(ModelSearchResult, error) bool) {
		b, ok := c.base.(BinarySearcher)
		if !ok {
			yield(ModelSearchResult{}, ErrUnsupported)
			return
		}
		b.SearchBinaryIter(ctx, v, top, opts)(yield)
	})
}
func (c *TypedModelCollection[K, T]) HybridSearchBinary(ctx context.Context, v BinaryVector, keywords string, top int, opts HybridSearchOptions) ([]TypedModelSearchResult[T], error) {
	b, ok := c.base.(BinaryHybridSearcher)
	if !ok {
		return nil, ErrUnsupported
	}
	return c.searchResults(ctx, func() ([]ModelSearchResult, error) { return b.HybridSearchBinary(ctx, v, keywords, top, opts) })
}
