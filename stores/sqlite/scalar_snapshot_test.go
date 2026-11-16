package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestScalarReadsPinMetadataAndPayloadsToOneSnapshot(t *testing.T) {
	for _, op := range []string{"query", "count"} {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "snapshot.db")
			s, err := Open(ctx, path, StoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			writer, err := Open(ctx, path, StoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { writer.Close() })
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Version", Type: vectordata.TypeString}, {Name: "Rank", Type: vectordata.TypeInt64}}}
			c, err := s.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			other, err := writer.GetModelCollection("records", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var before, after []vectordata.ModelRecord
			for i := range 10 {
				before = append(before, vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Version": "before", "Rank": int64(i)}})
				after = append(after, vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Version": "after", "Rank": int64(9 - i)}})
			}
			if _, err := c.Insert(ctx, before); err != nil {
				t.Fatal(err)
			}
			wrote := false
			var writeErr error
			connector := lookupConnector{base: s.db.Driver(), dsn: path + "?_txlock=immediate&_pragma=busy_timeout(0)", before: func(query string) {
				if wrote || !strings.Contains(query, "FROM __vectorstore_records r WHERE") {
					return
				}
				wrote = true
				deadline, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				_, writeErr = other.Upsert(deadline, after)
			}}
			if err := s.db.Close(); err != nil {
				t.Fatal(err)
			}
			s.db = sql.OpenDB(connector)
			s.db.SetMaxOpenConns(1)
			filter := vectordata.Eq(vectordata.Column("Version"), "before")
			if op == "query" {
				rows, err := c.Query(ctx, filter, 2, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "Rank"}}, Skip: 1})
				if err != nil || len(rows) != 2 || rows[0].Key != int64(1) || rows[1].Key != int64(2) || rows[0].Data["Version"] != "before" {
					t.Fatalf("mixed query snapshots: %+v %v", rows, err)
				}
			} else {
				count, err := c.Count(ctx, filter)
				if err != nil || count != 10 {
					t.Fatalf("mixed count snapshot: %d %v", count, err)
				}
			}
			if !wrote || writeErr != nil {
				t.Fatalf("writer blocked by read: wrote=%t err=%v", wrote, writeErr)
			}
			if count, err := other.Count(ctx, filter); err != nil || count != 0 {
				t.Fatalf("writer did not commit: %d %v", count, err)
			}
			if _, err := c.Upsert(ctx, before[:1]); err != nil {
				t.Fatalf("read leaked connection: %v", err)
			}
		})
	}
}

func TestScalarReadsCancellationDiscardsResultsAndReleasesConnection(t *testing.T) {
	for _, op := range []string{"query", "count"} {
		t.Run(op, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString}}})
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Title": "match"}}}); err != nil {
				t.Fatal(err)
			}
			queried := false
			connector := lookupConnector{base: s.db.Driver(), dsn: s.databaseName + "?_txlock=immediate&_pragma=busy_timeout(0)", before: func(query string) {
				if strings.Contains(query, "FROM __vectorstore_records r WHERE") {
					queried = true
					cancel()
				}
			}}
			if err := s.db.Close(); err != nil {
				t.Fatal(err)
			}
			s.db = sql.OpenDB(connector)
			s.db.SetMaxOpenConns(1)
			filter := vectordata.Eq(vectordata.Column("Title"), "match")
			if op == "query" {
				rows, err := c.Query(ctx, filter, 1, vectordata.QueryOptions{})
				if !errors.Is(err, context.Canceled) || rows != nil {
					t.Fatalf("canceled query: %+v %v", rows, err)
				}
			} else {
				count, err := c.Count(ctx, filter)
				if !errors.Is(err, context.Canceled) || count != 0 {
					t.Fatalf("canceled count: %d %v", count, err)
				}
			}
			if !queried {
				t.Fatal("did not cancel native query")
			}
			if _, err := c.Upsert(context.Background(), []vectordata.ModelRecord{{Key: "b", Data: map[string]any{"Title": "after"}}}); err != nil {
				t.Fatalf("canceled read leaked connection: %v", err)
			}
		})
	}
}
