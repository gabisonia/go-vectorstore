package sqlite_test

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestSquaredDistanceSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "squared.db")
	store, err := sqlite.Open(ctx, path, sqlite.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Dense", Dimensions: 2, Metric: vectordata.DistanceL2Squared, Precision: vectordata.VectorFloat16}, {Name: "Sparse", Dimensions: 1000000000, Kind: vectordata.VectorSparse, Metric: vectordata.DistanceL2Squared}}}
	c, err := store.EnsureModelCollection(ctx, "squared", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Vectors: map[string][]float32{"Dense": {3, 4}}, SparseVectors: map[string]vectordata.SparseVector{"Sparse": {Dimensions: 1000000000, Indices: []int{0, 999999999}, Values: []float32{3, 4}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openStore(t, path, sqlite.StoreOptions{})
	c, err = reopened.GetModelCollection("squared", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dense, err := c.Search(ctx, []float32{0, 0}, 1, vectordata.ModelSearchOptions{VectorProperty: "Dense"})
	if err != nil {
		t.Fatal(err)
	}
	sparse, err := c.(vectordata.SparseSearcher).SearchSparse(ctx, vectordata.SparseVector{Dimensions: 1000000000}, 1, vectordata.ModelSearchOptions{VectorProperty: "Sparse"})
	if err != nil {
		t.Fatal(err)
	}
	for _, hits := range [][]vectordata.ModelSearchResult{dense, sparse} {
		if len(hits) != 1 || hits[0].Record.Key != "a" || hits[0].Distance != 25 || math.Abs(hits[0].Score-1.0/26) > 1e-12 {
			t.Fatalf("squared units lost after reopen: %v", hits)
		}
	}
}
