package postgres

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestSparseTextCoordinatesAndValidation(t *testing.T) {
	p := vectordata.VectorProperty{Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 1_000_000_000, Metric: vectordata.DistanceL2}
	v := vectordata.SparseVector{Dimensions: p.Dimensions, Indices: []int{0, 999999999}, Values: []float32{1, -0.125}}
	text := sparseLiteral(v)
	if text != "{1:1,1000000000:-0.125}/1000000000" {
		t.Fatalf("one-based SQL coordinates: %s", text)
	}
	got, err := parseSparseText(text, p)
	if err != nil || !reflect.DeepEqual(got, v) {
		t.Fatalf("sparse text round trip: %+v, %v", got, err)
	}
	for _, text := range []string{"", "{}", "[]/1000000000", "{0:1}/1000000000", "{1:NaN}/1000000000", "{1:Infinity}/1000000000", "{1:1,1:2}/1000000000", "{2:1,1:2}/1000000000", "{1000000001:1}/1000000000", "{1}/1000000000", "{1:1}/x"} {
		if _, err := parseSparseText(text, p); !errors.Is(err, vectordata.ErrInvalidVector) {
			t.Fatalf("invalid sparse text accepted %q: %v", text, err)
		}
	}
	if _, err := parseSparseText("{1:1}/3", p); !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("stored sparse dimensions accepted: %v", err)
	}
}
