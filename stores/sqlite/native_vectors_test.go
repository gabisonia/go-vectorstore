//go:build sqlitevecprobe

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	_ "modernc.org/sqlite/vec"
)

func nativeVectorFixture(t *testing.T) (*Store, *Collection, string) {
	t.Helper()
	if !nativeL1Platform() {
		t.Skip("native L1 is intentionally disabled on this platform")
	}
	path := filepath.Join(t.TempDir(), "native.db")
	s, err := Open(context.Background(), path, StoreOptions{VectorExecution: VectorExecutionSQLiteVec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Version", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2, Metric: vectordata.DistanceL1}}}
	base, err := s.EnsureModelCollection(context.Background(), "records", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s, base.(*Collection), path
}

func nativeVectorRows() []vectordata.ModelRecord {
	rows := make([]vectordata.ModelRecord, 200)
	for i := range rows {
		x := float32(i)
		if i >= 5 && i < 85 {
			x = 5
		}
		rows[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Version": "before"}, Vectors: map[string][]float32{"V": {x, 0}}}
	}
	return rows
}

func TestNativeVectorCompleteBoundary(t *testing.T) {
	ctx := context.Background()
	s, c, _ := nativeVectorFixture(t)
	if _, err := c.Insert(ctx, nativeVectorRows()); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	// top 4 + Skip 3 ends inside an 80-record equal-distance group. All 85
	// records at/below that distance must reach exact Go ranking, not just 7.
	for r, err := range c.scanProjectedVectorsSQL(ctx, tx, c.definition.Vectors[0], nativeL1CandidatesSQL, c.name, "V", packVector([]float32{0, 0}), 6) {
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if r.Key.(int64) >= 85 {
			t.Fatalf("far vector passed boundary: %v", r.Key)
		}
		count++
	}
	tx.Rollback()
	if count != 85 {
		t.Fatalf("boundary candidates=%d, want 85", count)
	}
	hits, err := c.Search(ctx, []float32{0, 0}, 4, vectordata.ModelSearchOptions{Skip: 3})
	if err != nil || len(hits) != 4 {
		t.Fatalf("native search: %+v %v", hits, err)
	}
	for i, hit := range hits {
		if hit.Record.Key != int64(i+3) {
			t.Fatalf("native key tie order: %+v", hits)
		}
	}
}

func TestNativeVectorUsesOneWALSnapshot(t *testing.T) {
	if !nativeL1Platform() {
		t.Skip("native kernel is intentionally disabled on this platform")
	}
	ctx := context.Background()
	s, c, path := nativeVectorFixture(t)
	if _, err := c.Insert(ctx, nativeVectorRows()); err != nil {
		t.Fatal(err)
	}
	writer, err := Open(ctx, path, StoreOptions{VectorProjections: true})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	other, err := writer.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	queries := 0
	var writeErr error
	connector := lookupConnector{base: s.db.Driver(), dsn: path + "?_txlock=immediate&_pragma=foreign_keys(1)", before: func(query string) {
		if !strings.Contains(query, "vec_distance_L1") {
			return
		}
		queries++
		if queries != 1 {
			return
		}
		after := nativeVectorRows()
		for i := range after {
			after[i].Data["Version"] = "after"
			after[i].Vectors["V"] = []float32{99, 0}
		}
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, writeErr = other.Upsert(deadline, after)
	}}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = sql.OpenDB(connector)
	s.db.SetMaxOpenConns(1)
	hits, err := c.Search(ctx, []float32{0, 0}, 4, vectordata.ModelSearchOptions{Skip: 3, IncludeVectors: true})
	if err != nil || len(hits) != 4 || queries != 1 || writeErr != nil {
		t.Fatalf("native snapshot: hits=%d queries=%d writeErr=%v error=%v", len(hits), queries, writeErr, err)
	}
	for i, hit := range hits {
		if hit.Record.Key != int64(i+3) || hit.Record.Data["Version"] != "before" || hit.Record.Vectors["V"][0] != float32(min(i+3, 5)) {
			t.Fatalf("mixed native snapshot: %+v", hit)
		}
	}
	if _, err := c.Delete(ctx, []any{int64(199)}); err != nil {
		t.Fatalf("leaked read transaction: %v", err)
	}
}

func TestNativeVectorCancellationAndMalformedRows(t *testing.T) {
	ctx := context.Background()
	s, c, path := nativeVectorFixture(t)
	if _, err := c.Insert(ctx, nativeVectorRows()); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{{0}, packVector([]float32{float32(math.NaN()), 0}), packVector([]float32{float32(math.Inf(1)), 0})} {
		if _, err := s.db.Exec(`UPDATE __vectorstore_vector_values SET vector=? WHERE collection=? AND key=?`, bad, c.name, keyToken(int64(199))); err != nil {
			t.Fatal(err)
		}
		if hits, err := c.Search(ctx, []float32{0, 0}, 1, vectordata.ModelSearchOptions{}); err == nil || hits != nil {
			t.Fatalf("invalid off-page projection was discarded: %+v %v", hits, err)
		}
		if err := c.RebuildVectorProjections(ctx); err != nil {
			t.Fatalf("error leaked connection: %v", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	sawNative := false
	connector := lookupConnector{base: s.db.Driver(), dsn: path + "?_txlock=immediate&_pragma=foreign_keys(1)", before: func(query string) {
		if strings.Contains(query, "vec_distance_L1") {
			sawNative = true
			cancel()
		}
	}}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = sql.OpenDB(connector)
	s.db.SetMaxOpenConns(1)
	if hits, err := c.Search(canceled, []float32{0, 0}, 1, vectordata.ModelSearchOptions{}); hits != nil || !errors.Is(err, context.Canceled) || !sawNative {
		t.Fatalf("native cancellation: %+v %v query=%v", hits, err, sawNative)
	}
	if _, err := c.Delete(ctx, []any{int64(199)}); err != nil {
		t.Fatalf("cancellation leaked transaction: %v", err)
	}
}
