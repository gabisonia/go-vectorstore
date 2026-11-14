//go:build integration

package postgres

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

// Compare a named lookup with the previous listing strategy as namespaces grow.
func BenchmarkCollectionExists(b *testing.B) {
	for _, size := range []int{1, 1000} {
		b.Run(fmt.Sprintf("vector_collections%d", size), func(b *testing.B) {
			c, _ := modelFixture(b, 2, 0, false)
			ctx := context.Background()
			if _, err := c.store.EnsureModelCollection(ctx, "scalar", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{}); err != nil {
				b.Fatal(err)
			}
			// Populate catalog entries, without record/index work in the timed region.
			tx, err := c.store.pool.Begin(ctx)
			if err != nil {
				b.Fatal(err)
			}
			defer rollbackTx(ctx, tx)
			batch := new(pgx.Batch)
			for i := 1; i < size; i++ {
				batch.Queue("CREATE TABLE " + qualifiedTable(c.store.opts.Schema, fmt.Sprintf("other_%d", i)) + " (v vector(2))")
			}
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				b.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				b.Fatal(err)
			}
			for _, name := range []string{c.name, "scalar", "missing", "list_reference"} {
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						var exists bool
						var err error
						if name == "list_reference" {
							var names []string
							names, err = c.store.ListCollectionNames(ctx)
							exists = slices.Contains(names, c.name)
						} else {
							exists, err = c.store.CollectionExists(ctx, name)
						}
						if err != nil || exists != (name != "missing") {
							b.Fatalf("exists=%v: %v", exists, err)
						}
					}
				})
			}
		})
	}
}
