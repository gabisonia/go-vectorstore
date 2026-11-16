package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
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
	testKeyReadSnapshot(t, "get_many")
}

func TestKeyFilterReadsUseOneReadSnapshot(t *testing.T) {
	for _, op := range []string{"query", "count"} {
		t.Run(op, func(t *testing.T) { testKeyReadSnapshot(t, op) })
	}
}

func testKeyReadSnapshot(t *testing.T, op string) {
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
			if op == "count" {
				_, writeErr = other.Delete(deadline, keys)
			} else {
				_, writeErr = other.Upsert(deadline, after)
			}
		}
	}}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	store.db = sql.OpenDB(connector)
	store.db.SetMaxOpenConns(1)
	var rows []vectordata.ModelRecord
	var count int64
	switch op {
	case "get_many":
		rows, err = c.GetMany(ctx, keys, vectordata.RetrievalOptions{})
	case "query":
		rows, err = c.Query(ctx, vectordata.In(vectordata.Column("ID"), keys...), len(keys), vectordata.QueryOptions{})
	case "count":
		count, err = c.Count(ctx, vectordata.In(vectordata.Column("ID"), keys...))
	}
	if err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatalf("read snapshot reserved the writer lock: %v", writeErr)
	}
	if op == "count" && count != int64(len(before)) {
		t.Fatalf("mixed count snapshots: got %d, want %d", count, len(before))
	}
	if lookups != 2 || op != "count" && len(rows) != len(before) {
		t.Fatalf("expected two lookup chunks: queries=%d records=%d", lookups, len(rows))
	}
	for _, row := range rows {
		if row.Data["Version"] != "before" {
			t.Fatalf("mixed snapshots across chunks: %+v", row)
		}
	}
	if op == "count" {
		if count, err := other.Count(ctx, nil); err != nil || count != 0 {
			t.Fatalf("competing delete did not commit: %d %v", count, err)
		}
	} else {
		current, err := other.Get(ctx, keys[0], vectordata.RetrievalOptions{})
		if err != nil || current.Data["Version"] != "after" {
			t.Fatalf("competing writer did not commit: %+v %v", current, err)
		}
	}
	// The read transaction must be gone before results reach the caller.
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{after[0]}); err != nil {
		t.Fatalf("read transaction leaked: %v", err)
	}
}

func TestKeyFilterCancellationDiscardsPartialBatches(t *testing.T) {
	for _, op := range []string{"query", "count"} {
		t.Run(op, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}})
			records := make([]vectordata.ModelRecord, lookupBatchSize+2)
			keys := make([]any, len(records))
			for i := range records {
				keys[i] = int64(i)
				records[i] = vectordata.ModelRecord{Key: keys[i]}
			}
			if _, err := c.Insert(ctx, records); err != nil {
				t.Fatal(err)
			}
			queries := 0
			connector := lookupConnector{base: s.db.Driver(), dsn: s.databaseName + "?_txlock=immediate&_pragma=busy_timeout(0)", before: func(query string) {
				if strings.Contains(query, "__vectorstore_records") && strings.Contains(query, " IN (") {
					queries++
					if queries == 2 {
						cancel()
					}
				}
			}}
			if err := s.db.Close(); err != nil {
				t.Fatal(err)
			}
			s.db = sql.OpenDB(connector)
			s.db.SetMaxOpenConns(1)
			f := vectordata.In(vectordata.Column("ID"), keys...)
			if op == "query" {
				rows, err := c.Query(ctx, f, len(keys), vectordata.QueryOptions{})
				if !errors.Is(err, context.Canceled) || rows != nil {
					t.Fatalf("partial query: %+v %v", rows, err)
				}
			} else {
				count, err := c.Count(ctx, f)
				if !errors.Is(err, context.Canceled) || count != 0 {
					t.Fatalf("partial count: %d %v", count, err)
				}
			}
			if queries != 2 {
				t.Fatalf("did not cancel between batches: %d", queries)
			}
			if _, err := c.Insert(context.Background(), []vectordata.ModelRecord{{Key: int64(-1)}}); err != nil {
				t.Fatalf("canceled read leaked connection: %v", err)
			}
		})
	}
}
