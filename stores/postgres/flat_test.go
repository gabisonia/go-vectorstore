package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestFlatRejectedBeforeDatabaseAccess(t *testing.T) {
	s := &PostgresVectorStore{opts: DefaultStoreOptions()}
	for _, precision := range []vectordata.VectorPrecision{vectordata.VectorFloat32, vectordata.VectorFloat16} {
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 3, Precision: precision, Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodFlat}}}}
		if _, err := s.GetModelCollection("flat", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("FLAT model index accepted: %v", err)
		}
		if _, err := s.PlanMigration(context.Background(), "flat", d); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("FLAT migration accepted: %v", err)
		}
	}
	if err := testCollection().EnsureIndexes(context.Background(), vectordata.IndexOptions{Vector: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodFlat}}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("FLAT legacy index accepted: %v", err)
	}
}
