package conformance

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func runOptionalVectors(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !caps.OptionalVectors {
		t.Skip("optional vectors are not advertised")
	}
	ctx := context.Background()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing}}}
	// Exercise only advertised kinds/precisions. A dense-only third-party provider
	// can support optional vectors without implementing binary or sparse search.
	metric := vectordata.DistanceCosine
	if slices.Contains(caps.DistanceMetrics, vectordata.DistanceL2) {
		metric = vectordata.DistanceL2
	}
	dense := len(caps.VectorKinds) == 0 || slices.Contains(caps.VectorKinds, vectordata.VectorDense)
	if dense {
		d.Vectors = append(d.Vectors, vectordata.VectorProperty{Name: "Dense", Dimensions: 2, Optional: true, Metric: metric})
		if slices.Contains(caps.VectorPrecisions, vectordata.VectorFloat16) {
			d.Vectors = append(d.Vectors, vectordata.VectorProperty{Name: "Half", Dimensions: 2, Optional: true, Precision: vectordata.VectorFloat16, Metric: metric})
		}
	}
	if slices.Contains(caps.VectorKinds, vectordata.VectorBinary) {
		d.Vectors = append(d.Vectors, vectordata.VectorProperty{Name: "Bits", Dimensions: 2, Optional: true, Kind: vectordata.VectorBinary})
	}
	if slices.Contains(caps.VectorKinds, vectordata.VectorSparse) {
		d.Vectors = append(d.Vectors, vectordata.VectorProperty{Name: "Sparse", Dimensions: 2, Optional: true, Kind: vectordata.VectorSparse, Metric: metric})
	}
	if len(d.Vectors) == 0 {
		t.Fatal("optional vectors advertised without a supported kind")
	}
	c, err := factory(t).EnsureModelCollection(ctx, "optional_vectors", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bits, err := vectordata.ParseBinaryVector("10")
	if err != nil {
		t.Fatal(err)
	}
	indices, values := []int{0}, []float32{1}
	vector := []float32{1, 0}
	if metric == vectordata.DistanceL2 {
		indices, values = nil, nil
		vector = []float32{0, 0}
	}
	sparse, err := vectordata.NewSparseVector(2, indices, values)
	if err != nil {
		t.Fatal(err)
	}
	full := vectordata.ModelRecord{Key: "full", Data: map[string]any{"Body": "alpha"}, Vectors: map[string][]float32{}, BinaryVectors: map[string]vectordata.BinaryVector{}, SparseVectors: map[string]vectordata.SparseVector{}}
	for _, p := range d.Vectors {
		switch p.Kind {
		case vectordata.VectorBinary:
			full.BinaryVectors[p.Name] = bits
		case vectordata.VectorSparse:
			full.SparseVectors[p.Name] = sparse
		default:
			full.Vectors[p.Name] = vector
		}
	}
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
	search := func(p vectordata.VectorProperty) ([]vectordata.ModelSearchResult, error) {
		opts := vectordata.ModelSearchOptions{VectorProperty: p.Name, IncludeVectors: true}
		switch p.Kind {
		case vectordata.VectorBinary:
			return c.(vectordata.BinarySearcher).SearchBinary(ctx, bits, 5, opts)
		case vectordata.VectorSparse:
			return c.(vectordata.SparseSearcher).SearchSparse(ctx, sparse, 5, opts)
		default:
			return c.Search(ctx, vector, 5, opts)
		}
	}
	for _, p := range d.Vectors {
		hits, err := search(p)
		if err != nil || len(hits) != 1 || hits[0].Record.Key != "full" {
			t.Fatalf("%s missing/zero vector search: %v, %v", p.Name, hits, err)
		}
	}
	if caps.HybridSearch && dense {
		hits, err := c.(vectordata.HybridSearcher).HybridSearch(ctx, vector, "alpha", 5, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{VectorProperty: "Dense"}})
		if err != nil || len(hits) != 1 || hits[0].Record.Key != "full" {
			t.Fatalf("hybrid NULL exclusion: %v, %v", hits, err)
		}
	}
	for _, p := range d.Vectors {
		bad := vectordata.ModelRecord{Key: "bad", Data: empty.Data}
		switch p.Kind {
		case vectordata.VectorBinary:
			bad.BinaryVectors = map[string]vectordata.BinaryVector{p.Name: {}}
		case vectordata.VectorSparse:
			bad.SparseVectors = map[string]vectordata.SparseVector{p.Name: {}}
		default:
			bad.Vectors = map[string][]float32{p.Name: {}}
		}
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
	for _, p := range d.Vectors {
		hits, err := search(p)
		if err != nil || len(hits) != 0 {
			t.Fatalf("clear %s: %v, %v", p.Name, hits, err)
		}
	}
	if count, err := c.Count(ctx, nil); err != nil || count != 2 {
		t.Fatalf("NULL rows lost: %d %v", count, err)
	}
}
