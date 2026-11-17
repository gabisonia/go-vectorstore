//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIntegrationOperationMetadataDistinguishesNamespaces(t *testing.T) {
	ctx := context.Background()
	pool := integrationPool(t)
	stores := []*PostgresVectorStore{newTestStore(t, pool), newTestStore(t, pool)}
	if stores[0].opts.Schema == stores[1].opts.Schema {
		t.Fatal("fixture namespaces are not distinct")
	}
	for _, store := range stores {
		c, err := store.EnsureModelCollection(ctx, "same_collection", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "same_key"}}); err != nil {
			t.Fatal(err)
		}
		_, err = c.Insert(ctx, []vectordata.ModelRecord{{Key: "same_key"}})
		var operation *vectordata.OperationError
		var native *pgconn.PgError
		if !errors.Is(err, vectordata.ErrInvalidRecord) || !errors.As(err, &operation) || !errors.As(err, &native) || native.Code != "23505" {
			t.Fatalf("native duplicate-key failure: %v", err)
		}
		if operation.StoreMetadata != store.StoreMetadata() || operation.DatabaseName != pool.Config().ConnConfig.Database || operation.Namespace != store.opts.Schema || operation.SystemName != "postgresql" || operation.Collection != "same_collection" || operation.Operation != "write" {
			t.Fatalf("namespace identity: %#v", operation)
		}
	}
}
