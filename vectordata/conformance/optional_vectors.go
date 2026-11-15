package conformance

import (
	"context"
	"errors"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"testing"
)

func runOptionalVectors(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !caps.OptionalVectors {
		t.Skip("optional vectors are not advertised")
	}
	ctx := context.Background()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}}}
	d.Vectors = []vectordata.VectorProperty{
		{Name: "Dense", Dimensions: 2, Optional: true, Index: &vectordata.VectorIndexOptions{}},
		{Name: "Half", Dimensions: 2, Optional: true, Precision: vectordata.VectorFloat16},
		{Name: "Bits", Dimensions: 2, Optional: true, Kind: vectordata.VectorBinary},
		{Name: "Sparse", Dimensions: 2, Optional: true, Kind: vectordata.VectorSparse},
	}
	c, err := factory(t).EnsureModelCollection(ctx, "optional_vectors", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bits, err := vectordata.ParseBinaryVector("10")
	if err != nil {
		t.Fatal(err)
	}
	sparse, err := vectordata.NewSparseVector(2, []int{0}, []float32{1})
	if err != nil {
		t.Fatal(err)
	}
	full := vectordata.ModelRecord{Key: "full", Data: map[string]any{"Body": "alpha"}, Vectors: map[string][]float32{"Dense": {1, 0}, "Half": {1, 0}}, BinaryVectors: map[string]vectordata.BinaryVector{"Bits": bits}, SparseVectors: map[string]vectordata.SparseVector{"Sparse": sparse}}
	empty := vectordata.ModelRecord{Key: "empty", Data: map[string]any{"Body": "alpha"}}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{empty, full}); err != nil {
		t.Fatal(err)
	}
	row, err := c.Get(ctx, "empty", vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Vectors {
		if vectordata.HasModelVector(row, p) {
			t.Fatalf("missing vector became present: %s", p.Name)
		}
	}
	for _, name := range []string{"Dense", "Half", "Bits", "Sparse"} {
		opts := vectordata.ModelSearchOptions{VectorProperty: name, IncludeVectors: true}
		var hits []vectordata.ModelSearchResult
		switch name {
		case "Bits":
			hits, err = c.(vectordata.BinarySearcher).SearchBinary(ctx, bits, 5, opts)
		case "Sparse":
			hits, err = c.(vectordata.SparseSearcher).SearchSparse(ctx, sparse, 5, opts)
		default:
			hits, err = c.Search(ctx, []float32{1, 0}, 5, opts)
		}
		if err != nil || len(hits) != 1 || hits[0].Record.Key != "full" {
			t.Fatalf("%s missing-vector search: %v, %v", name, hits, err)
		}
	}
	if caps.HybridSearch {
		hits, err := c.(vectordata.HybridSearcher).HybridSearch(ctx, []float32{1, 0}, "alpha", 5, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{VectorProperty: "Dense"}})
		if err != nil || len(hits) != 1 || hits[0].Record.Key != "full" {
			t.Fatalf("hybrid NULL exclusion: %v, %v", hits, err)
		}
	}
	for _, bad := range []vectordata.ModelRecord{
		{Key: "bad", Data: empty.Data, Vectors: map[string][]float32{"Dense": {}}},
		{Key: "bad", Data: empty.Data, Vectors: map[string][]float32{"Half": {1}}},
		{Key: "bad", Data: empty.Data, BinaryVectors: map[string]vectordata.BinaryVector{"Bits": {}}},
		{Key: "bad", Data: empty.Data, SparseVectors: map[string]vectordata.SparseVector{"Sparse": {}}},
	} {
		if _, err := c.Upsert(ctx, []vectordata.ModelRecord{empty, bad}); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("malformed optional vector accepted: %v", err)
		}
	}
	full.Vectors = nil
	full.BinaryVectors = nil
	full.SparseVectors = nil
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{full}); err != nil {
		t.Fatal(err)
	}
	hits, err := c.Search(ctx, []float32{1, 0}, 5, vectordata.ModelSearchOptions{VectorProperty: "Dense"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("clear vector: %v, %v", hits, err)
	}
	if count, err := c.Count(ctx, nil); err != nil || count != 2 {
		t.Fatalf("NULL rows lost: %d %v", count, err)
	}
}
