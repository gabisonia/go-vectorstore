package vectordata_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestBinaryVectorPackingAndValidation(t *testing.T) {
	for _, text := range []string{"0", "1", "101", "10100000", "101010101", strings.Repeat("1", 64000)} {
		v, err := vectordata.ParseBinaryVector(text)
		if err != nil {
			t.Fatal(err)
		}
		got, err := v.BitString()
		if err != nil || got != text || v.Dimensions != len(text) {
			t.Fatalf("binary packing: %q, %v", got, err)
		}
		p := vectordata.VectorProperty{Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: len(text), Metric: vectordata.DistanceHamming}
		owned, err := vectordata.NormalizeBinaryVector(v, p)
		if err != nil {
			t.Fatal(err)
		}
		v.Bytes[0] ^= 0xff
		if owned.Bytes[0] == v.Bytes[0] {
			t.Fatal("normalized bits alias input")
		}
	}
	for _, text := range []string{"", "102", " 101", "101 ", "１", strings.Repeat("0", 64001)} {
		if _, err := vectordata.ParseBinaryVector(text); err == nil {
			t.Fatalf("accepted bits %q", text)
		}
	}
	for _, v := range []vectordata.BinaryVector{{}, {Dimensions: 3}, {Bytes: []byte{0x81}, Dimensions: 3}, {Bytes: []byte{0}, Dimensions: 9}, {Bytes: []byte{0, 0}, Dimensions: 3}} {
		if _, err := v.BitString(); err == nil {
			t.Fatalf("accepted invalid packed vector: %+v", v)
		}
	}
	a, _ := vectordata.ParseBinaryVector("101010101")
	b, _ := vectordata.ParseBinaryVector("111000100")
	if n, err := vectordata.HammingDistance(a, b); err != nil || n != 3 {
		t.Fatalf("Hamming: %d, %v", n, err)
	}
	short, _ := vectordata.ParseBinaryVector("101")
	if _, err := vectordata.NormalizeBinaryVector(short, vectordata.VectorProperty{Kind: vectordata.VectorBinary, Dimensions: 8}); !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("same-byte different-length accepted: %v", err)
	}
	if _, err := vectordata.HammingDistance(short, a); !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("different dimensions: %v", err)
	}
}

func TestBinaryDefinitionAndCodec(t *testing.T) {
	type model struct {
		ID    string                  `vectordata:"key"`
		Bits  vectordata.BinaryVector `vectordata:"vector,kind=binary,dimensions=3,index"`
		Dense []float32               `vectordata:"vector,dimensions=2"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	d := codec.Definition()
	if d.Vectors[0].Metric != vectordata.DistanceHamming || d.Vectors[0].Kind != vectordata.VectorBinary {
		t.Fatalf("binary defaults: %+v", d)
	}
	v, _ := vectordata.ParseBinaryVector("101")
	r, err := codec.Encode(model{ID: "a", Bits: v, Dense: []float32{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	v.Bytes[0] = 0
	if r.BinaryVectors["Bits"].Bytes[0] != 0xa0 {
		t.Fatal("codec aliases binary input")
	}
	got, err := codec.Decode(r)
	if err != nil || !reflect.DeepEqual(got.Bits, r.BinaryVectors["Bits"]) {
		t.Fatalf("binary decoding: %+v, %v", got, err)
	}
	got.Bits.Bytes[0] = 0
	if r.BinaryVectors["Bits"].Bytes[0] != 0xa0 {
		t.Fatal("decoded bits alias record")
	}
	dense := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Dense", Kind: vectordata.VectorDense, Dimensions: 2}}}
	dense, err = dense.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(dense)
	if strings.Contains(string(payload), "Kind") {
		t.Fatal("default kind changed stored definitions")
	}
	for _, p := range []vectordata.VectorProperty{
		{Kind: vectordata.VectorBinary, Dimensions: 3, Metric: vectordata.DistanceL2},
		{Kind: vectordata.VectorBinary, Dimensions: 3, Precision: vectordata.VectorFloat32},
		{Kind: vectordata.VectorBinary, Dimensions: 3, SourceProperty: "Text"},
		{Kind: vectordata.VectorBinary, Dimensions: 64001},
		{Dimensions: 3, Metric: vectordata.DistanceHamming},
	} {
		p.Name = "V"
		if _, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{p}}).Normalize(); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("invalid binary definition: %+v, %v", p, err)
		}
	}
	type wrong struct {
		ID   string `vectordata:"key"`
		Bits []byte `vectordata:"vector,kind=binary,dimensions=3"`
	}
	if _, err := vectordata.NewStructCodec[wrong](nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("missing bit length accepted: %v", err)
	}
}

func TestInvalidBinaryBatchSkipsFloatEmbeddings(t *testing.T) {
	d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "Dense", Dimensions: 2, SourceProperty: "Body"}, {Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 3}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	embedder := vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return [][]float32{{1, 0}}, nil })
	_, err = vectordata.PrepareModelRecords(context.Background(), d, vectordata.ModelCollectionOptions{Embedder: embedder}, []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Body": "alpha"}, BinaryVectors: map[string]vectordata.BinaryVector{"Bits": {Bytes: []byte{1}, Dimensions: 3}}}})
	if !errors.Is(err, vectordata.ErrInvalidVector) || calls != 0 {
		t.Fatalf("invalid bits invoked embedding: %d calls, %v", calls, err)
	}
}

type noBinaryCollection struct{ vectordata.ModelCollection }

func TestTypedBinaryOptionalProvider(t *testing.T) {
	type model struct {
		ID string `vectordata:"key"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := memory.New().GetModelCollection("optional", codec.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	typed, err := vectordata.NewTypedModelCollection[string, model](noBinaryCollection{base}, codec)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := vectordata.ParseBinaryVector("0")
	if _, err := typed.SearchBinary(context.Background(), v, 1, vectordata.ModelSearchOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("optional binary provider: %v", err)
	}
	for _, err := range typed.SearchBinaryIter(context.Background(), v, 1, vectordata.ModelSearchOptions{}) {
		if !errors.Is(err, vectordata.ErrUnsupported) {
			t.Fatal(err)
		}
	}
	if _, err := typed.HybridSearchBinary(context.Background(), v, "alpha", 1, vectordata.HybridSearchOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatal(err)
	}
}
