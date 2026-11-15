package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestSearchUsesOneReadSnapshot(t *testing.T) {
	ctx := context.Background()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Version", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 1, Metric: vectordata.DistanceL2}}}
	store, c := lookupFixture(t, d)
	// The fixture's native connection knows the filename through SQLite itself.
	var seq int
	var name, path string
	rows, err := store.db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatal("missing database")
	}
	err = rows.Scan(&seq, &name, &path)
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Open(ctx, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	other, err := writer.GetModelCollection(c.name, d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := make([]vectordata.ModelRecord, 20)
	after := make([]vectordata.ModelRecord, len(before))
	for i := range before {
		key := fmt.Sprintf("key-%03d", i)
		before[i] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Version": "before"}, Vectors: map[string][]float32{"V": {1}}}
		after[i] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Version": "after"}, Vectors: map[string][]float32{"V": {9}}}
	}
	if _, err := c.Insert(ctx, before); err != nil {
		t.Fatal(err)
	}
	wrote := false
	var writeErr error
	connector := lookupConnector{base: store.db.Driver(), dsn: path + "?_txlock=immediate&_pragma=busy_timeout(0)", before: func(query string) {
		if wrote || !strings.HasPrefix(query, "SELECT payload FROM") {
			return
		}
		wrote = true
		deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_, writeErr = other.Upsert(deadline, after)
	}}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	store.db = sql.OpenDB(connector)
	store.db.SetMaxOpenConns(1)
	hits, err := c.Search(ctx, []float32{1}, 3, vectordata.ModelSearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !wrote || writeErr != nil {
		t.Fatalf("writer did not commit during read snapshot: wrote=%t error=%v", wrote, writeErr)
	}
	for i, hit := range hits {
		if hit.Record.Key != fmt.Sprintf("key-%03d", i) || hit.Record.Data["Version"] != "before" || hit.Distance != 0 {
			t.Fatalf("mixed search snapshot: %+v", hits)
		}
	}
	if len(hits) != 3 {
		t.Fatalf("result count: %d", len(hits))
	}
	current, err := other.Get(ctx, "key-000", vectordata.RetrievalOptions{})
	if err != nil || current.Data["Version"] != "after" {
		t.Fatalf("writer changed no records: %+v %v", current, err)
	}
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{after[0]}); err != nil {
		t.Fatalf("search leaked read transaction: %v", err)
	}
}

func TestSearchScanErrorsDiscardResultsAndReleaseConnection(t *testing.T) {
	ctx := context.Background()
	s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 1, Metric: vectordata.DistanceL2}}})
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "a", Vectors: map[string][]float32{"V": {1}}}, {Key: "z", Vectors: map[string][]float32{"V": {2}}}}); err != nil {
		t.Fatal(err)
	}
	bad, err := encodeRecord(vectordata.ModelRecord{Key: "z", Data: map[string]any{}, Vectors: map[string][]float32{"V": {2, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE __vectorstore_records SET payload=? WHERE collection=? AND key=?`, bad, c.name, keyToken("z")); err != nil {
		t.Fatal(err)
	}
	hits, err := c.Search(ctx, []float32{1}, 1, vectordata.ModelSearchOptions{})
	if hits != nil || !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("partial results/error: %+v %v", hits, err)
	}
	if _, err := c.Delete(ctx, []any{"z"}); err != nil {
		t.Fatalf("read connection leaked: %v", err)
	}
	hits, err = c.Search(ctx, []float32{1}, 1, vectordata.ModelSearchOptions{})
	if err != nil || len(hits) != 1 {
		t.Fatalf("search after error: %+v %v", hits, err)
	}
}
