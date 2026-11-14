package postgres

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestVectorBinaryEncoding(t *testing.T) {
	vector := writeVector{1, -2.5, math.Float32frombits(0x80000000), math.SmallestNonzeroFloat32, math.MaxFloat32}
	prefix := []byte{0xaa, 0xbb}
	encoded, err := (vectorEncodePlan{format: pgtype.BinaryFormatCode}).Encode(vector, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded[:2], prefix) || binary.BigEndian.Uint16(encoded[2:4]) != 5 || binary.BigEndian.Uint16(encoded[4:6]) != 0 {
		t.Fatalf("invalid prefix/header: %x", encoded)
	}
	for i, value := range vector {
		if got := binary.BigEndian.Uint32(encoded[6+4*i:]); got != math.Float32bits(value) {
			t.Fatalf("element %d bits=%x, want=%x", i, got, math.Float32bits(value))
		}
	}
	text, err := (vectorEncodePlan{format: pgtype.TextFormatCode}).Encode(writeVector{1, -2.5}, nil)
	if err != nil || string(text) != "[1,-2.5]" {
		t.Fatalf("text protocol: %q, %v", text, err)
	}
	for _, vector := range []writeVector{nil, make(writeVector, math.MaxInt16+1)} {
		if _, err := (vectorEncodePlan{format: pgtype.BinaryFormatCode}).Encode(vector, nil); !errors.Is(err, vectordata.ErrInvalidVector) {
			t.Fatalf("invalid dimensions: %v", err)
		}
	}
}

func BenchmarkVectorParameterEncoding(b *testing.B) {
	for _, dimensions := range []int{128, 1536} {
		vector := writeVector(benchmarkRecords(1, dimensions)[0].Vector)
		for _, format := range []int16{pgtype.TextFormatCode, pgtype.BinaryFormatCode} {
			b.Run(map[int16]string{0: "text", 1: "binary"}[format]+"/"+strconv.Itoa(dimensions), func(b *testing.B) {
				plan := vectorEncodePlan{format: format}
				buf := make([]byte, 0, 4+16*dimensions)
				b.ReportAllocs()
				arg := any(vector)
				for b.Loop() {
					var err error
					buf, err = plan.Encode(arg, buf[:0])
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestBinaryWriteSelectionBounds(t *testing.T) {
	for _, tt := range []struct {
		rows, dimensions int
		want             bool
	}{
		{0, 1536, false}, {1, 0, false}, {-1, 1536, false}, {1, -1, false},
		{1, 128, false}, {7, 128, false}, {8, 128, true}, {1, 1536, true},
		{math.MaxInt, 1, true}, {1, math.MaxInt, true},
	} {
		if got := useBinaryVectors(tt.rows, tt.dimensions); got != tt.want {
			t.Fatalf("rows=%d, dimensions=%d: binary=%v, want=%v", tt.rows, tt.dimensions, got, tt.want)
		}
	}
}
