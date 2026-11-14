package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestMigrationRejectsInvalidInputBeforeDatabaseAccess(t *testing.T) {
	store := new(PostgresVectorStore)
	good := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}
	for _, tt := range []struct {
		name       string
		definition vectordata.CollectionDefinition
	}{
		{"", good}, {"__vectorstore_models", good}, {"valid", vectordata.CollectionDefinition{}},
		{"duplicate_indexes", vectordata.CollectionDefinition{Key: good.Key, Vectors: []vectordata.VectorProperty{{Name: "First", Dimensions: 3, Index: &vectordata.VectorIndexOptions{Name: "shared"}}, {Name: "Second", Dimensions: 3, Index: &vectordata.VectorIndexOptions{Name: "shared"}}}}},
	} {
		if _, err := store.PlanMigration(context.Background(), tt.name, tt.definition); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatalf("invalid input: %v", err)
		}
	}
}
