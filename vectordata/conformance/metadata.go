package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func runMetadata(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !caps.Metadata {
		t.Skip("metadata not advertised")
	}
	store := factory(t)
	provider, ok := store.(vectordata.StoreMetadataProvider)
	if !ok {
		t.Fatal("advertised store metadata interface missing")
	}
	wantStore := provider.StoreMetadata()
	if wantStore.SystemName == "" {
		t.Fatal("system name missing")
	}
	c, err := store.GetModelCollection("metadata_handle", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(vectordata.CollectionMetadataProvider); !ok {
		t.Fatal("advertised collection metadata interface missing")
	}
	want := vectordata.CollectionMetadata{StoreMetadata: wantStore, CollectionName: c.Name()}
	if got := vectordata.GetCollectionMetadata(c); got != want {
		t.Fatalf("handle metadata: %+v", got)
	}
	ctx := context.Background()
	if exists, err := c.Exists(ctx); err != nil || exists {
		t.Fatalf("metadata created storage: %t, %v", exists, err)
	}
	if err := c.EnsureExists(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "missing", vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrNotFound) {
		t.Fatalf("missing record: %v", err)
	} else {
		// Existing providers may omit both wrapping and error metadata. When
		// supplied, identity must agree with the collection's local metadata.
		var operation *vectordata.OperationError
		if errors.As(err, &operation) && operation.StoreMetadata != (vectordata.StoreMetadata{}) && (operation.StoreMetadata != wantStore || operation.Collection != c.Name() || operation.Operation == "") {
			t.Fatalf("operation identity differs from collection: %+v", operation)
		}
	}
	if got := vectordata.GetCollectionMetadata(c); got != want {
		t.Fatalf("created collection metadata: %+v", got)
	}
	if err := c.EnsureDeleted(ctx); err != nil {
		t.Fatal(err)
	}
	if got := vectordata.GetCollectionMetadata(c); got != want {
		t.Fatalf("deleted collection metadata: %+v", got)
	}
}
