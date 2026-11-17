package vectordata_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestFlatIndexDefinition(t *testing.T) {
	type taggedModel struct {
		ID     string    `vectordata:"key"`
		Vector []float32 `vectordata:"vector,dimensions=2,method=flat"`
	}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodFlat}}}}
	before := d.Clone()
	normalized, err := d.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	index := normalized.Vectors[0].Index
	if index.Method != vectordata.IndexMethodFlat || index.Metric != vectordata.DistanceCosine || index.HNSW != (vectordata.HNSWOptions{}) || index.IVFFlat != (vectordata.IVFFlatOptions{}) || !reflect.DeepEqual(d, before) {
		t.Fatalf("FLAT normalization/ownership: %+v %+v", index, d)
	}
	fromTags, err := vectordata.DefinitionFor[taggedModel]()
	if err != nil || !reflect.DeepEqual(fromTags, normalized) {
		t.Fatalf("FLAT tag differs from explicit definition: %+v %v", fromTags, err)
	}
	for _, mutate := range []func(*vectordata.VectorProperty){
		func(p *vectordata.VectorProperty) { p.Index.HNSW.M = 16 },
		func(p *vectordata.VectorProperty) { p.Index.HNSW.EfConstruction = 64 },
		func(p *vectordata.VectorProperty) { p.Index.IVFFlat.Lists = 10 },
		func(p *vectordata.VectorProperty) { p.Index.Metric = vectordata.DistanceL2 },
		func(p *vectordata.VectorProperty) { p.Kind = vectordata.VectorSparse },
	} {
		bad := d.Clone()
		mutate(&bad.Vectors[0])
		if _, err := bad.Normalize(); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("invalid FLAT definition accepted: %+v %v", bad, err)
		}
	}
}

func TestFlatHintsRetainPortableExactSearch(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "flat.db"), sqlite.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for name, store := range map[string]vectordata.ModelStore{"memory": memory.New(), "sqlite": s} {
		t.Run(name, func(t *testing.T) {
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodFlat}}}}
			c, err := store.EnsureModelCollection(ctx, "flat", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Vectors: map[string][]float32{"Vector": {0, 0}}}, {Key: "b", Vectors: map[string][]float32{"Vector": {1, 0}}}}); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, []float32{1, 0}, 2, vectordata.ModelSearchOptions{})
			if err != nil || len(hits) != 2 || hits[0].Record.Key != "b" || hits[0].Distance != 0 || hits[1].Record.Key != "a" || hits[1].Distance != 1 {
				t.Fatalf("portable FLAT hint: %+v %v", hits, err)
			}
			if len(store.(vectordata.CapabilityProvider).Capabilities().VectorIndexMethods) != 0 {
				t.Fatal("portable hint advertised physical indexing")
			}
		})
	}
}
