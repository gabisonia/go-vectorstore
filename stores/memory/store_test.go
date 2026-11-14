package memory_test

import (
	"context"
	"errors"
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/conformance"
	"testing"
)

func TestModelContract(t *testing.T) {
	conformance.Run(t, func(t *testing.T) vectordata.ModelStore { return memory.New() })
}

func TestEmbeddingConfiguration(t *testing.T) {
	conformance.RunEmbeddingConfiguration(t, func(t *testing.T, defaults vectordata.ModelCollectionOptions) vectordata.ModelStore {
		store, err := memory.NewWithOptions(memory.StoreOptions{Embedder: defaults.Embedder, EmbeddingBatchSize: defaults.EmbeddingBatchSize})
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func TestEmbeddingOptionsValidation(t *testing.T) {
	if _, err := memory.NewWithOptions(memory.StoreOptions{EmbeddingBatchSize: -1}); !errors.Is(err, vectordata.ErrInvalidEmbeddingOptions) {
		t.Fatalf("invalid defaults: %v", err)
	}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyString}}
	store := memory.New()
	if _, err := store.EnsureModelCollection(context.Background(), "invalid", d, vectordata.ModelCollectionOptions{EmbeddingBatchSize: -1}); !errors.Is(err, vectordata.ErrInvalidEmbeddingOptions) {
		t.Fatalf("invalid options: %v", err)
	}
	if exists, err := store.CollectionExists(context.Background(), "invalid"); err != nil || exists {
		t.Fatalf("invalid options created storage: %v, %v", exists, err)
	}
}
