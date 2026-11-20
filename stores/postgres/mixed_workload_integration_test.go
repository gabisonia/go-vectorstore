//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/internal/mixedworkload"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Hold the target row on another connection so cancellation occurs after the
// server starts the upsert. Other workers use different keys in the same table.
func checkMixedCancellation(ctx context.Context, c *ModelCollection, row vectordata.ModelRecord) error {
	pool := c.store.pool
	locker, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig)
	if err != nil {
		return err
	}
	defer locker.Close(ctx)
	tx, err := locker.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	if _, err = tx.Exec(ctx, "SELECT 1 FROM "+c.table()+` WHERE "ID"=$1 FOR UPDATE`, row.Key); err != nil {
		return err
	}
	cfg := pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer single.Close()
	store, err := NewVectorStore(single, c.store.opts)
	if err != nil {
		return err
	}
	probe, err := store.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	var pid uint32
	if err = single.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		return err
	}
	row.Data = map[string]any{"Eligible": false, "Revision": int64(-1)}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := probe.Upsert(op, []vectordata.ModelRecord{row}); done <- err }()
	blocked := waitForBlockedBackend(op, pool, pid)
	cancel()
	outcome := <-done
	if blocked != nil {
		return blocked
	}
	if !errors.Is(outcome, context.Canceled) {
		return fmt.Errorf("blocked upsert returned %v", outcome)
	}
	if err = tx.Rollback(ctx); err != nil {
		return err
	}
	got, err := probe.Get(ctx, row.Key, vectordata.RetrievalOptions{})
	if err == nil && got.Data["Revision"] != int64(0) {
		return fmt.Errorf("canceled upsert changed %v", row.Key)
	}
	return err
}

func TestIntegrationMixedVectorWorkload(t *testing.T) {
	ctx := t.Context()
	pool := integrationPool(t)
	report := mixedworkload.Run(t, os.Getenv("PGVECTOR_MIXED_MEASURE") == "1", newTestStore(t, pool), func(c vectordata.ModelCollection) error {
		_, err := pool.Exec(ctx, "ANALYZE "+c.(*ModelCollection).table())
		return err
	}, func(c vectordata.ModelCollection, row vectordata.ModelRecord) error {
		return checkMixedCancellation(ctx, c.(*ModelCollection), row)
	})
	if report == nil {
		return
	}
	var pg, extension string
	if err := pool.QueryRow(ctx, "SELECT current_setting('server_version'), extversion FROM pg_extension WHERE extname='vector'").Scan(&pg, &extension); err != nil {
		t.Fatal(err)
	}
	report["postgres"], report["pgvector"] = pg, extension
	if integrationContainer != nil {
		var bytes int64
		if err := pool.QueryRow(ctx, "SELECT pg_read_file('/sys/fs/cgroup/memory.current')::bigint").Scan(&bytes); err != nil {
			t.Fatal(err)
		}
		report["database_cgroup_bytes"] = bytes
	}
	mixedworkload.Log(t, report)
}
