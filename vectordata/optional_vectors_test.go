package vectordata_test

import (
	"context"
	"errors"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"testing"
)

type nullableModel struct {
	ID           string                   `vectordata:"key"`
	Dense        []float32                `vectordata:"vector,dimensions=2,optional"`
	DensePointer *[]float32               `vectordata:"vector,dimensions=2"`
	Binary       *vectordata.BinaryVector `vectordata:"vector,dimensions=2,kind=binary"`
	Sparse       *vectordata.SparseVector `vectordata:"vector,dimensions=2,kind=sparse"`
}

func TestNullableVectorCodec(t *testing.T) {
	codec, err := vectordata.NewStructCodec[nullableModel](nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range codec.Definition().Vectors {
		if !p.Optional {
			t.Fatalf("nullable property inferred as required: %s", p.Name)
		}
	}
	record, err := codec.Encode(nullableModel{ID: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := vectordata.PrepareModelRecords(context.Background(), codec.Definition(), vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{record})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(prepared[0])
	if err != nil || decoded.Dense != nil || decoded.DensePointer != nil || decoded.Binary != nil || decoded.Sparse != nil {
		t.Fatalf("NULL pointers: %+v %v", decoded, err)
	}
	dense := []float32{1, 0}
	bits, _ := vectordata.ParseBinaryVector("10")
	sparse, _ := vectordata.NewSparseVector(2, []int{0}, []float32{1})
	record, err = codec.Encode(nullableModel{ID: "full", Dense: dense, DensePointer: &dense, Binary: &bits, Sparse: &sparse})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = codec.Decode(record)
	if err != nil || decoded.Binary == nil || decoded.Sparse == nil || decoded.DensePointer == nil {
		t.Fatalf("nonnull pointers: %+v %v", decoded, err)
	}
	decoded.Dense[0] = 7
	(*decoded.DensePointer)[0] = 8
	decoded.Binary.Bytes[0] = 0
	decoded.Sparse.Values[0] = 9
	if record.Vectors["Dense"][0] != 1 || record.Vectors["DensePointer"][0] != 1 || record.BinaryVectors["Binary"].Bytes[0] == 0 || record.SparseVectors["Sparse"].Values[0] != 1 {
		t.Fatal("decoded vectors alias encoded values")
	}
}
func TestOptionalEmbeddingSource(t *testing.T) {
	ctx := context.Background()
	calls := 0
	d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Text", Type: vectordata.TypeString, Optional: true}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Optional: true, SourceProperty: "Text"}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	options := vectordata.ModelCollectionOptions{Embedder: vectordata.EmbeddingFunc(func(context.Context, []string) ([][]float32, error) { calls++; return [][]float32{{1, 0}}, nil })}
	prepared, err := vectordata.PrepareModelRecords(ctx, d, options, []vectordata.ModelRecord{{Key: "null"}, {Key: "embedded", Data: map[string]any{"Text": "hello"}}})
	if err != nil || calls != 1 || prepared[0].Vectors["Vector"] != nil || len(prepared[1].Vectors["Vector"]) != 2 {
		t.Fatalf("optional embedding: %v calls=%d %v", prepared, calls, err)
	}
	_, err = vectordata.PrepareModelRecords(ctx, d, options, []vectordata.ModelRecord{{Key: "bad", Data: map[string]any{"Text": "hello"}, Vectors: map[string][]float32{"Vector": {}}}})
	if !errors.Is(err, vectordata.ErrDimensionMismatch) || calls != 1 {
		t.Fatalf("malformed explicit vector must not trigger embedding: %v calls=%d", err, calls)
	}
	d.Vectors[0].Optional = false
	if _, err = vectordata.PrepareModelRecords(ctx, d, options, []vectordata.ModelRecord{{Key: "required"}}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("missing required source: %v", err)
	}
}
