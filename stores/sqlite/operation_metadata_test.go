package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
	driver "modernc.org/sqlite"
)

func TestOperationMetadataPreservesNativeAndSentinelErrors(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, filepath.Join(t.TempDir(), "operations.db"), sqlite.StoreOptions{})
	c, err := store.EnsureModelCollection(ctx, "docs", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	check := func(err error, op string) *vectordata.OperationError {
		t.Helper()
		var got *vectordata.OperationError
		if !errors.As(err, &got) || got.StoreMetadata != store.StoreMetadata() || got.Backend != "sqlite" || got.Collection != "docs" || got.Operation != op {
			t.Fatalf("operation metadata: %+v", got)
		}
		return got
	}
	_, err = c.Get(ctx, "missing", vectordata.RetrievalOptions{})
	check(err, "get")
	if !errors.Is(err, vectordata.ErrNotFound) {
		t.Fatal("lost not-found sentinel")
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "one"}}); err != nil {
		t.Fatal(err)
	}
	_, err = c.Insert(ctx, []vectordata.ModelRecord{{Key: "one"}})
	wrapped := check(err, "write")
	var cause *driver.Error
	if !errors.Is(err, vectordata.ErrInvalidRecord) || !errors.As(err, &cause) {
		t.Fatalf("lost native constraint error: %v", err)
	}
	wrapped.DatabaseName = "changed"
	if store.StoreMetadata().DatabaseName == "changed" {
		t.Fatal("error metadata changed store identity")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = c.Get(canceled, "one", vectordata.RetrievalOptions{})
	check(err, "get")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation cause")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(ctx, "one", vectordata.RetrievalOptions{})
	check(err, "get")
}
