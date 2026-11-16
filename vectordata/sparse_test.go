package vectordata_test

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestSparseValidationAndOwnership(t *testing.T) {
	indices, values := []int{0, 4, 999999999}, []float32{1, 0, -2}
	v, err := vectordata.NewSparseVector(1_000_000_000, indices, values)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Indices, []int{0, 999999999}) || !reflect.DeepEqual(v.Values, []float32{1, -2}) {
		t.Fatalf("sparse canonicalization: %+v", v)
	}
	indices[0] = 2
	values[0] = 99
	if v.Indices[0] != 0 || v.Values[0] != 1 {
		t.Fatal("sparse constructor aliases inputs")
	}
	p := vectordata.VectorProperty{Name: "V", Kind: vectordata.VectorSparse, Dimensions: v.Dimensions}
	copy, err := vectordata.NormalizeSparseVector(v, p)
	if err != nil {
		t.Fatal(err)
	}
	copy.Indices[0] = 3
	copy.Values[0] = 22
	if v.Indices[0] != 0 || v.Values[0] != 1 {
		t.Fatal("normalized sparse vector aliases inputs")
	}
	for _, bad := range []vectordata.SparseVector{
		{}, {Dimensions: 1_000_000_001}, {Dimensions: 3, Indices: []int{0}, Values: nil},
		{Dimensions: 3, Indices: []int{-1}, Values: []float32{1}}, {Dimensions: 3, Indices: []int{3}, Values: []float32{1}},
		{Dimensions: 3, Indices: []int{1, 1}, Values: []float32{1, 2}}, {Dimensions: 3, Indices: []int{2, 1}, Values: []float32{1, 2}},
		{Dimensions: 3, Indices: []int{0}, Values: []float32{float32(math.NaN())}}, {Dimensions: 3, Indices: []int{0}, Values: []float32{float32(math.Inf(1))}},
	} {
		if _, err := vectordata.NewSparseVector(bad.Dimensions, bad.Indices, bad.Values); err == nil {
			t.Fatalf("accepted invalid sparse vector: %+v", bad)
		}
	}
	zero := vectordata.SparseVector{Dimensions: p.Dimensions}
	if _, err := vectordata.NormalizeSparseVector(zero, p); !errors.Is(err, vectordata.ErrInvalidVector) {
		t.Fatalf("zero cosine sparse vector: %v", err)
	}
	p.Metric = vectordata.DistanceL2
	if got, err := vectordata.NormalizeSparseVector(zero, p); err != nil || len(got.Values) != 0 {
		t.Fatalf("zero L2 sparse vector: %+v, %v", got, err)
	}
	p.Dimensions--
	if _, err := vectordata.NormalizeSparseVector(v, p); !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("sparse dimension mismatch: %v", err)
	}
}

func TestSparseNonzeroLimits(t *testing.T) {
	indices, values := make([]int, 16001), make([]float32, 16001)
	for i := range indices {
		indices[i] = i
		values[i] = 1
	}
	p := vectordata.VectorProperty{Kind: vectordata.VectorSparse, Dimensions: 1_000_000_000, Metric: vectordata.DistanceL2}
	v := vectordata.SparseVector{Dimensions: p.Dimensions, Indices: indices[:16000], Values: values[:16000]}
	if _, err := vectordata.NormalizeSparseVector(v, p); err != nil {
		t.Fatal(err)
	}
	if _, err := vectordata.NewSparseVector(p.Dimensions, indices, values); !errors.Is(err, vectordata.ErrInvalidVector) {
		t.Fatalf("over storage limit accepted: %v", err)
	}
	p.Index = &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}
	v.Indices, v.Values = indices[:1000], values[:1000]
	if _, err := vectordata.NormalizeSparseVector(v, p); err != nil {
		t.Fatal(err)
	}
	v.Indices, v.Values = indices[:1001], values[:1001]
	if _, err := vectordata.NormalizeSparseVector(v, p); !errors.Is(err, vectordata.ErrInvalidVector) {
		t.Fatalf("over index limit accepted: %v", err)
	}
	values[1000] = 0
	if got, err := vectordata.NormalizeSparseVector(v, p); err != nil || len(got.Values) != 1000 {
		t.Fatalf("zeros counted as nonzero: %+v, %v", got, err)
	}
}

func TestSparseDistancesAgainstDenseOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 29))
	for trial := 0; trial < 32; trial++ {
		a, b := make([]float32, 64), make([]float32, 64)
		a[0] = 1
		b[1] = 1
		for i := 2; i < 64; i++ {
			if rng.IntN(3) == 0 {
				a[i] = float32(rng.IntN(9) - 4)
			}
			if rng.IntN(3) == 0 {
				b[i] = float32(rng.IntN(9) - 4)
			}
		}
		sparse := func(x []float32) vectordata.SparseVector {
			v := vectordata.SparseVector{Dimensions: 1_000_000_000}
			for i, value := range x {
				if value != 0 {
					v.Indices = append(v.Indices, i)
					v.Values = append(v.Values, value)
				}
			}
			return v
		}
		var sum, l1, dot, aa, bb float64
		for i, x := range a {
			y := float64(b[i])
			xx := float64(x)
			delta := xx - y
			sum += delta * delta
			l1 += math.Abs(delta)
			dot += xx * y
			aa += xx * xx
			bb += y * y
		}
		for metric, want := range map[vectordata.DistanceMetric]float64{vectordata.DistanceL2: math.Sqrt(sum), vectordata.DistanceL2Squared: sum, vectordata.DistanceL1: l1, vectordata.DistanceInnerProduct: -dot, vectordata.DistanceCosine: 1 - dot/math.Sqrt(aa*bb)} {
			got, err := vectordata.SparseDistance(sparse(a), sparse(b), metric)
			if err != nil || math.Abs(got-want) > 1e-12 {
				t.Fatalf("%s sparse distance: %g want %g, %v", metric, got, want, err)
			}
		}
	}
}

func TestSparseCodecAndEarlyValidation(t *testing.T) {
	type model struct {
		ID     string                  `vectordata:"key"`
		Sparse vectordata.SparseVector `vectordata:"vector,kind=sparse,dimensions=1000000000"`
		Dense  []float32               `vectordata:"vector,dimensions=2"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	v := vectordata.SparseVector{Dimensions: 1_000_000_000, Indices: []int{5}, Values: []float32{2}}
	r, err := codec.Encode(model{ID: "a", Sparse: v, Dense: []float32{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	v.Indices[0] = 9
	v.Values[0] = 3
	got, err := codec.Decode(r)
	if err != nil || got.Sparse.Indices[0] != 5 || got.Sparse.Values[0] != 2 {
		t.Fatalf("sparse codec: %+v, %v", got, err)
	}
	got.Sparse.Indices[0] = 4
	got.Sparse.Values[0] = 3
	if r.SparseVectors["Sparse"].Indices[0] != 5 || r.SparseVectors["Sparse"].Values[0] != 2 {
		t.Fatal("sparse decoding aliases record")
	}
	for _, p := range []vectordata.VectorProperty{{Kind: vectordata.VectorSparse, Dimensions: 3, Metric: vectordata.DistanceHamming}, {Kind: vectordata.VectorSparse, Dimensions: 3, Precision: vectordata.VectorFloat16}, {Kind: vectordata.VectorSparse, Dimensions: 3, SourceProperty: "Body"}, {Kind: vectordata.VectorSparse, Dimensions: 3, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodIVFFlat}}, {Kind: vectordata.VectorSparse, Dimensions: 1_000_000_001}} {
		p.Name = "V"
		if _, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{p}}).Normalize(); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("bad sparse definition: %+v, %v", p, err)
		}
	}
	d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "Dense", Dimensions: 2, SourceProperty: "Body"}, {Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 3}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	embedder := vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return [][]float32{{1, 0}}, nil })
	_, err = vectordata.PrepareModelRecords(context.Background(), d, vectordata.ModelCollectionOptions{Embedder: embedder}, []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Body": "alpha"}, SparseVectors: map[string]vectordata.SparseVector{"Sparse": {Dimensions: 3, Indices: []int{0, 0}, Values: []float32{1, 2}}}}})
	if !errors.Is(err, vectordata.ErrInvalidVector) || calls != 0 {
		t.Fatalf("invalid sparse vector invoked embedder: %d, %v", calls, err)
	}
	base, err := memory.New().GetModelCollection("optional", codec.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	typed, err := vectordata.NewTypedModelCollection[string, model](noBinaryCollection{base}, codec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := typed.SearchSparse(context.Background(), v, 1, vectordata.ModelSearchOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatal(err)
	}
	for _, err := range typed.SearchSparseIter(context.Background(), v, 1, vectordata.ModelSearchOptions{}) {
		if !errors.Is(err, vectordata.ErrUnsupported) {
			t.Fatal(err)
		}
	}
	if _, err := typed.HybridSearchSparse(context.Background(), v, "alpha", 1, vectordata.HybridSearchOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatal(err)
	}
}
