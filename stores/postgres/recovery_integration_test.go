//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func waitForBlockedBackend(ctx context.Context, pool *pgxpool.Pool, pid uint32) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')", pid).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func TestIntegrationConnectionLossRollbackAndPoolRecovery(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			control := integrationPool(t)
			cfg, err := pgxpool.ParseConfig(integrationDSN)
			if err != nil {
				t.Fatal(err)
			}
			cfg.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			store := newTestStore(t, pool)
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Rank", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 3, Metric: vectordata.DistanceL2}}}
			base, err := store.EnsureModelCollection(ctx, "fault", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			c := base.(*ModelCollection)
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "kept", Data: map[string]any{"Rank": int64(-1)}, Vectors: map[string][]float32{"V": {1, 0, 0}}}}); err != nil {
				t.Fatal(err)
			}
			var pid uint32
			if err := pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			blocker, err := control.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackTx(ctx, blocker)
			if operation == "read" {
				if _, err := blocker.Exec(ctx, "LOCK TABLE "+c.table()+" IN ACCESS EXCLUSIVE MODE"); err != nil {
					t.Fatal(err)
				}
			} else {
				// The second batch blocks after the first 500 inserts executed. Killing
				// the backend must roll back the entire write, including that first batch.
				key := time.Now().UnixNano()
				function := quoteIdent(store.opts.Schema) + "." + quoteIdent("block_last_row")
				statement := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW."Rank"=500 THEN PERFORM pg_advisory_xact_lock(%d); END IF; RETURN NEW; END $$`, function, key)
				if _, err := control.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
				if _, err := control.Exec(ctx, "CREATE TRIGGER block_last BEFORE INSERT ON "+c.table()+" FOR EACH ROW EXECUTE FUNCTION "+function+"()"); err != nil {
					t.Fatal(err)
				}
				if _, err := blocker.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", key); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				if operation == "read" {
					_, err := c.Count(ctx, nil)
					done <- err
					return
				}
				records := make([]vectordata.ModelRecord, 501)
				for i := range records {
					records[i] = vectordata.ModelRecord{Key: fmt.Sprintf("pending-%03d", i), Data: map[string]any{"Rank": int64(i)}, Vectors: map[string][]float32{"V": {1, 0, 0}}}
				}
				keys, err := c.Insert(ctx, records)
				if err != nil && len(keys) != 0 {
					done <- fmt.Errorf("failed write returned partial keys: %v", keys)
					return
				}
				done <- err
			}()
			if err := waitForBlockedBackend(ctx, control, pid); err != nil {
				t.Fatal(err)
			}
			var killed bool
			if err := control.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&killed); err != nil || !killed {
				t.Fatalf("terminate backend: %t %v", killed, err)
			}
			select {
			case err := <-done:
				var provider *vectordata.OperationError
				var native *pgconn.PgError
				if !errors.As(err, &provider) || !errors.As(err, &native) || native.Code != "57P01" {
					t.Fatalf("lost provider/native failure: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("operation did not return after connection termination")
			}
			if err := blocker.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if count, err := c.Count(ctx, nil); err != nil || count != 1 {
				t.Fatalf("rollback/pool recovery: count=%d %v", count, err)
			}
			if _, err := c.Get(ctx, "pending-000", vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrNotFound) {
				t.Fatalf("partial batch committed: %v", err)
			}
			if _, err := c.Upsert(ctx, []vectordata.ModelRecord{{Key: "after", Data: map[string]any{"Rank": int64(501)}, Vectors: map[string][]float32{"V": {1, 0, 0}}}}); err != nil {
				t.Fatalf("subsequent write: %v", err)
			}
			var replacement uint32
			if err := pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&replacement); err != nil || replacement == pid {
				t.Fatalf("connection was not replaced: %d %v", replacement, err)
			}
			if hits, err := c.Search(ctx, []float32{1, 0, 0}, 2, vectordata.ModelSearchOptions{IncludeVectors: true}); err != nil || len(hits) != 2 {
				t.Fatalf("search after reconnect: %d %v", len(hits), err)
			}
		})
	}
}
