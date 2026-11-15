package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Exclusive locking covers reads and connection-setup PRAGMAs, unlike an ordinary
// WAL writer. Close idle provider connections so the external connection can
// obtain the exclusive file lock before the operation reconnects.
func TestExclusiveReadDeadlines(t *testing.T) {
	for _, operation := range []string{"list", "exists", "get", "get_many", "query", "count", "search", "open"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "exclusive.db")
			store, err := Open(ctx, path, StoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 1, Optional: true, Metric: vectordata.DistanceL2}}}
			c, err := store.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "kept"}}); err != nil {
				t.Fatal(err)
			}
			store.db.SetMaxIdleConns(0)
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { raw.Close() })
			raw.SetMaxIdleConns(0)
			lock, err := raw.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { lock.Close() })
			if _, err := lock.ExecContext(ctx, "PRAGMA locking_mode=EXCLUSIVE"); err != nil {
				t.Fatal(err)
			}
			if _, err := lock.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
				t.Fatal(err)
			}
			deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			switch operation {
			case "list":
				_, err = store.ListCollectionNames(deadline)
			case "exists":
				_, err = store.CollectionExists(deadline, "records")
			case "get":
				_, err = c.Get(deadline, "kept", vectordata.RetrievalOptions{})
			case "get_many":
				_, err = c.GetMany(deadline, []any{"kept"}, vectordata.RetrievalOptions{})
			case "query":
				_, err = c.Query(deadline, nil, 1, vectordata.QueryOptions{})
			case "search":
				_, err = c.Search(deadline, []float32{0}, 1, vectordata.ModelSearchOptions{})
			case "count":
				_, err = c.Count(deadline, nil)
			case "open":
				var other *Store
				other, err = Open(deadline, path, StoreOptions{})
				if other != nil {
					other.Close()
				}
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("deadline error lost: %v", err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("50ms read deadline blocked for %v", elapsed)
			}
			// Closing with max idle zero releases the exclusive lock even after rollback.
			if _, err := lock.ExecContext(ctx, "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			if n, err := c.Count(ctx, nil); err != nil || n != 1 {
				t.Fatalf("connection/data lost after read timeout: %d %v", n, err)
			}
		})
	}
}
