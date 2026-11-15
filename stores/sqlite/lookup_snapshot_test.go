package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Intercept a real driver's SELECT calls so a second connection can commit
// between lookup chunks, without adding timing hooks to production code.
type lookupConnector struct {
	base   driver.Driver
	dsn    string
	before func(string)
}

func (c lookupConnector) Driver() driver.Driver { return c.base }
func (c lookupConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &lookupConn{Conn: conn, before: c.before}, nil
}

type lookupConn struct {
	driver.Conn
	before func(string)
}

func (c *lookupConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *lookupConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.before(query)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func TestGetManyUsesOneReadSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	store, err := Open(ctx, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	writer, err := Open(ctx, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Version", Type: vectordata.TypeString}}}
	c, err := store.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := writer.GetModelCollection("records", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := make([]vectordata.ModelRecord, lookupBatchSize+1)
	after := make([]vectordata.ModelRecord, len(before))
	keys := make([]any, len(before))
	for i := range before {
		key := fmt.Sprintf("key-%04d", i)
		keys[i] = key
		before[i] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Version": "before"}}
		after[i] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Version": "after"}}
	}
	if _, err := c.Insert(ctx, before); err != nil {
		t.Fatal(err)
	}
	lookups := 0
	var writeErr error
	connector := lookupConnector{base: store.db.Driver(), dsn: path + "?_txlock=immediate&_pragma=busy_timeout(0)", before: func(query string) {
		if !strings.Contains(query, "__vectorstore_records") || !strings.Contains(query, " IN (") {
			return
		}
		lookups++
		if lookups == 2 {
			// A read-only/deferred transaction permits this WAL write. An accidental
			// immediate transaction would block it until its context expires.
			deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			_, writeErr = other.Upsert(deadline, after)
		}
	}}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	store.db = sql.OpenDB(connector)
	store.db.SetMaxOpenConns(1)
	rows, err := c.GetMany(ctx, keys, vectordata.RetrievalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatalf("read snapshot reserved the writer lock: %v", writeErr)
	}
	if lookups != 2 || len(rows) != len(before) {
		t.Fatalf("expected two lookup chunks: queries=%d records=%d", lookups, len(rows))
	}
	for _, row := range rows {
		if row.Data["Version"] != "before" {
			t.Fatalf("mixed snapshots across chunks: %+v", row)
		}
	}
	current, err := other.Get(ctx, keys[0], vectordata.RetrievalOptions{})
	if err != nil || current.Data["Version"] != "after" {
		t.Fatalf("competing writer did not commit: %+v %v", current, err)
	}
	// The read transaction must be gone before results reach the caller.
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{after[0]}); err != nil {
		t.Fatalf("read transaction leaked: %v", err)
	}
}
