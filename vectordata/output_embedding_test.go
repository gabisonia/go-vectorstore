package vectordata_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestFloat16BitsExhaustiveWidening(t *testing.T) {
	for bits := range 1 << 16 {
		got := vectordata.Float16Bits(bits).Float32()
		exponent, mantissa := (bits>>10)&31, bits&1023
		if exponent == 31 {
			if mantissa != 0 {
				if !math.IsNaN(float64(got)) {
					t.Fatalf("%04x: expected NaN", bits)
				}
				continue
			}
			sign := 1
			if bits&0x8000 != 0 {
				sign = -1
			}
			if !math.IsInf(float64(got), sign) {
				t.Fatalf("%04x: expected signed infinity", bits)
			}
			continue
		}
		// Independent arithmetic oracle; the implementation normalizes bitfields.
		var want float32
		if exponent == 0 {
			want = float32(math.Ldexp(float64(mantissa), -24))
		} else {
			want = float32(math.Ldexp(1+float64(mantissa)/1024, exponent-15))
		}
		if bits&0x8000 != 0 {
			want = -want
		}
		if math.Float32bits(got) != math.Float32bits(want) {
			t.Fatalf("%04x: got %08x want %08x", bits, math.Float32bits(got), math.Float32bits(want))
		}
	}
}

func TestOutputAdapterValidationAndOwnership(t *testing.T) {
	var nilFunc vectordata.OutputEmbeddingFunc[string, vectordata.Float16Bits]
	if _, err := vectordata.AdaptOutputEmbedder(nilFunc); !errors.Is(err, vectordata.ErrInvalidEmbeddingOptions) {
		t.Fatal(err)
	}
	var calls int
	buffer := []vectordata.Float16Bits{0x0001, 0x8000, 0x3e00}
	source := vectordata.OutputEmbeddingFunc[string, vectordata.Float16Bits](func(context.Context, []string) ([][]vectordata.Float16Bits, error) {
		calls++
		return [][]vectordata.Float16Bits{buffer}, nil
	})
	adapter, err := vectordata.AdaptOutputEmbedder(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []any{nil, 1, (*string)(nil)} {
		if _, err := adapter.EmbedInputs(t.Context(), []any{input}); !errors.Is(err, vectordata.ErrInvalidEmbeddingOptions) {
			t.Fatal(err)
		}
	}
	if out, err := adapter.EmbedInputs(t.Context(), nil); err != nil || len(out) != 0 || calls != 0 {
		t.Fatalf("empty/invalid inputs invoked generator: %v %v %d", out, err, calls)
	}
	out, err := adapter.EmbedInputs(t.Context(), []any{"text"})
	if err != nil || out[0][0] != float32(math.Ldexp(1, -24)) || math.Float32bits(out[0][1]) != 0x80000000 || out[0][2] != 1.5 {
		t.Fatalf("widening: %v %v", out, err)
	}
	buffer[0] = 0x3c00
	if out[0][0] != float32(math.Ldexp(1, -24)) {
		t.Fatal("output aliases generator buffer")
	}
	out[0][2] = 99
	if buffer[2] != 0x3e00 {
		t.Fatal("caller output changed generator buffer")
	}
	if _, err := adapter.EmbedInputs(t.Context(), []any{"a", "b"}); !errors.Is(err, vectordata.ErrInvalidVector) {
		t.Fatal("accepted wrong count:", err)
	}
	for _, invalid := range []vectordata.Float16Bits{0x7c00, 0xfc00, 0x7c01, 0x7fff} {
		buffer[0] = invalid
		if out, err := adapter.EmbedInputs(t.Context(), []any{"a"}); out != nil || !errors.Is(err, vectordata.ErrInvalidVector) {
			t.Fatalf("accepted %04x: %v %v", invalid, out, err)
		}
	}
}

func TestOutputAdapterFloat32AliasesErrorsAndCancellation(t *testing.T) {
	type sample float32
	native := errors.New("generator unavailable")
	buffer := []sample{1.5, -2}
	adapter, err := vectordata.AdaptOutputEmbedder(vectordata.OutputEmbeddingFunc[int, sample](func(ctx context.Context, inputs []int) ([][]sample, error) {
		if inputs[0] == 0 {
			return nil, native
		}
		return [][]sample{buffer}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.EmbedInputs(t.Context(), []any{0}); !errors.Is(err, native) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := adapter.EmbedInputs(ctx, []any{1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	out, err := adapter.EmbedInputs(t.Context(), []any{1})
	if err != nil || out[0][0] != 1.5 {
		t.Fatal(out, err)
	}
	buffer[0] = 2
	if out[0][0] != 1.5 {
		t.Fatal("float32 buffer aliases source")
	}
	for _, vector := range [][]sample{nil, {sample(math.NaN())}, {sample(math.Inf(1))}} {
		buffer = vector
		if out, err := adapter.EmbedInputs(t.Context(), []any{1}); out != nil || !errors.Is(err, vectordata.ErrInvalidVector) {
			t.Fatal(out, err)
		}
	}
	ctx, cancel = context.WithCancel(t.Context())
	canceled, err := vectordata.AdaptOutputEmbedder(vectordata.OutputEmbeddingFunc[string, float32](func(context.Context, []string) ([][]float32, error) { cancel(); return nil, native }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canceled.EmbedInputs(ctx, []any{"a"}); !errors.Is(err, context.Canceled) || !errors.Is(err, native) {
		t.Fatal(err)
	}
}

func TestOutputAdapterConcurrentUse(t *testing.T) {
	adapter, err := vectordata.AdaptOutputEmbedder(vectordata.OutputEmbeddingFunc[string, vectordata.Float16Bits](func(context.Context, []string) ([][]vectordata.Float16Bits, error) {
		return [][]vectordata.Float16Bits{{0x3c00, 0x3800}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := adapter.EmbedInputs(t.Context(), []any{"a"})
			if err != nil || out[0][1] != 0.5 {
				t.Error(out, err)
			}
			out[0][0] = 2
		}()
	}
	wg.Wait()
}

func ExampleAdaptOutputEmbedder() {
	source := vectordata.OutputEmbeddingFunc[string, vectordata.Float16Bits](func(context.Context, []string) ([][]vectordata.Float16Bits, error) {
		return [][]vectordata.Float16Bits{{0x3c00, 0x3800}}, nil
	})
	adapter, err := vectordata.AdaptOutputEmbedder(source)
	if err != nil {
		panic(err)
	}
	vectors, err := adapter.EmbedInputs(context.Background(), []any{"hello"})
	if err != nil {
		panic(err)
	}
	fmt.Println(vectors)
	// Output: [[1 0.5]]
}
