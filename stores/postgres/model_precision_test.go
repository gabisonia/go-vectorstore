package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestVectorPrecisionDimensionLimits(t *testing.T) {
	store := &PostgresVectorStore{opts: DefaultStoreOptions()}
	for _, tt := range []struct {
		name             string
		precision        vectordata.VectorPrecision
		dimensions       int
		indexed, invalid bool
	}{
		{"full_index_max", vectordata.VectorFloat32, 2000, true, false},
		{"full_index_overflow", vectordata.VectorFloat32, 2001, true, true},
		{"half_index_max", vectordata.VectorFloat16, 4000, true, false},
		{"half_index_overflow", vectordata.VectorFloat16, 4001, true, true},
		{"half_storage_max", vectordata.VectorFloat16, 16000, false, false},
		{"half_storage_overflow", vectordata.VectorFloat16, 16001, false, true},
		{"full_storage_overflow", vectordata.VectorFloat32, 16001, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := vectordata.VectorProperty{Name: "Vector", Dimensions: tt.dimensions, Precision: tt.precision}
			if tt.indexed {
				p.Index = &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW}
			}
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{p}}
			_, err := store.GetModelCollection("precision", d, vectordata.ModelCollectionOptions{})
			if tt.invalid != errors.Is(err, vectordata.ErrSchemaMismatch) || !tt.invalid && err != nil {
				t.Fatalf("dimension validation: %v", err)
			}
		})
	}
}

func TestHalfVectorIndexOperatorClasses(t *testing.T) {
	for _, metric := range []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceL2Squared, vectordata.DistanceInnerProduct} {
		for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat} {
			d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 3, Precision: vectordata.VectorFloat16, Metric: metric, Index: &vectordata.VectorIndexOptions{Method: method}}}}).Normalize()
			if err != nil {
				t.Fatal(err)
			}
			indexes, err := modelIndexSpecs("precision", d)
			if err != nil || len(indexes) != 1 || !strings.HasPrefix(indexes[0].opClass, "halfvec_") {
				t.Fatalf("half index: %+v, %v", indexes, err)
			}
		}
	}
}
