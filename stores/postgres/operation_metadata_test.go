package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOperationMetadataRetainsConfiguredStoreAndCauses(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://private_user:private_password@127.0.0.1:1/research?application_name=private_option")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store, err := NewVectorStore(pool, StoreOptions{Schema: `tenant"one`})
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}}
	handle, err := store.GetModelCollection("docs", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := handle.(*ModelCollection)
	check := func(err error, op string) *vectordata.OperationError {
		t.Helper()
		var got *vectordata.OperationError
		if !errors.As(err, &got) || got.StoreMetadata != store.StoreMetadata() || got.Backend != "postgres" || got.Collection != "docs" || got.Operation != op {
			t.Fatalf("operation metadata: %+v", got)
		}
		return got
	}
	_, err = c.Get(ctx, "key", vectordata.RetrievalOptions{})
	check(err, "get")
	_, err = store.PlanMigration(ctx, "docs", d)
	check(err, "plan_migration")
	err = store.ApplyMigration(ctx, vectordata.MigrationPlan{Collection: "docs"})
	check(err, "apply_migration")
	if !errors.Is(err, vectordata.ErrMigrationConflict) {
		t.Fatalf("lost migration sentinel: %v", err)
	}
	// Exercise native classification through the same collection wrapper used
	// by reads and iterators, without depending on a live server.
	native := &pgconn.PgError{Code: "42P01", Message: "missing relation"}
	err = c.operation("query", native)
	wrapped := check(err, "query")
	var cause *pgconn.PgError
	if !errors.Is(err, vectordata.ErrCollectionNotFound) || !errors.As(err, &cause) || cause != native {
		t.Fatalf("lost native/sentinel causes: %v", err)
	}
	wrapped.DatabaseName = "changed"
	if store.StoreMetadata().DatabaseName != "research" {
		t.Fatal("error metadata changed store identity")
	}
	if c.operation("get", nil) != nil || store.operation("docs", "get", nil) != nil {
		t.Fatal("nil error was wrapped")
	}
	err = c.operation("search", context.Canceled)
	check(err, "search")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation cause")
	}
}
