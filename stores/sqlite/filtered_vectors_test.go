package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func openVectorSearchFixture(t testing.TB, path string, d vectordata.CollectionDefinition, projections bool) (*Store, *Collection) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, path, StoreOptions{NativeIndexes: projections, VectorProjections: projections})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	base, err := s.EnsureModelCollection(ctx, "records", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s, base.(*Collection)
}

func TestIndexedVectorEquality(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "filter.db")
	d := scalarIndexDefinition()
	d.Vectors = []vectordata.VectorProperty{{Name: "V", Dimensions: 2, Metric: vectordata.DistanceL2, Optional: true}}
	s, c := openVectorSearchFixture(t, path, d, true)
	_, canonical := openVectorSearchFixture(t, path, d, false)
	const number = int64(1<<53 + 1)
	var rows []vectordata.ModelRecord
	for i := range 8 {
		r := scalarIndexRow(int64(i), number)
		if i%2 == 0 {
			r.Data["Number"] = nil
		}
		if i == 7 {
			r.Data["Number"] = number - 1
		}
		if i != 1 {
			r.Vectors = map[string][]float32{"V": {1, 0}}
		}
		rows = append(rows, r)
	}
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// Assert the actual plan uses both the equality value and the full packed key.
	plan, err := s.db.Query("EXPLAIN QUERY PLAN "+indexedVectorEqualitySQL, c.name, "Number", number, "V")
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for plan.Next() {
		var id, parent, aux int
		var detail string
		if err := plan.Scan(&id, &parent, &aux, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := plan.Err(); err != nil {
		t.Fatal(err)
	}
	plan.Close()
	explain := strings.Join(details, "\n")
	for _, want := range []string{"__vectorstore_scalar_lookup (collection=? AND property=? AND value=?)", "PRIMARY KEY (collection=? AND property=? AND key=?)"} {
		if !strings.Contains(explain, want) {
			t.Fatalf("missing %q in plan:\n%s", want, explain)
		}
	}
	scans := 0
	var onScan func()
	connector := lookupConnector{base: s.db.Driver(), dsn: path + "?_pragma=foreign_keys(1)", before: func(query string) {
		if query == indexedVectorEqualitySQL {
			scans++
			if onScan != nil {
				onScan()
			}
		}
	}}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = sql.OpenDB(connector)
	s.db.SetMaxOpenConns(1)
	check := func(filter vectordata.Filter, indexed bool) {
		t.Helper()
		before := scans
		opts := vectordata.ModelSearchOptions{Filter: filter, Skip: 1, IncludeVectors: true}
		q := []float32{0, 0}
		want, err := canonical.Search(ctx, q, 2, opts)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Search(ctx, q, 2, opts)
		if err != nil || !reflect.DeepEqual(want, got) || (scans == before+1) != indexed {
			t.Fatalf("filter %#v indexed=%v scans=%d: got %v, %v want %v", filter, indexed, scans-before, got, err, want)
		}
	}
	eq := func(field string, value any) vectordata.Filter { return vectordata.Eq(vectordata.Column(field), value) }
	for _, f := range []vectordata.Filter{eq("Number", number), eq("Number", nil), eq("Number", int64(-42)), eq("Active", true), eq("Amount", math.Nextafter(1, 2)), eq(scalarTextName, "literal'\x00雪")} {
		check(f, true)
	}
	check(eq("Unindexed", "x"), false)
	check(vectordata.Not(eq("Number", number)), false)
	// Commit an unaware write between the readiness checks and the vector scan.
	// The current result must still contain the old filtered page and payloads.
	onScan = func() {
		rows[5].Data["Number"] = nil
		rows[3].Vectors = map[string][]float32{"V": {9, 9}}
		if _, err := canonical.Upsert(ctx, rows); err != nil {
			t.Fatal(err)
		}
	}
	check(eq("Number", number), true)
	onScan = nil
	check(eq("Number", number), false)
	if err := c.RebuildVectorProjections(ctx); err != nil {
		t.Fatal(err)
	}
	check(eq("Number", number), true)
	if _, err := s.db.Exec("DELETE FROM __vectorstore_scalar_indexes WHERE collection=?", c.name); err != nil {
		t.Fatal(err)
	}
	check(eq("Number", number), false)
}

// Warm L1 top 10 on 100k int64 keys, 128 dimensions and a 4 KiB body. Both
// handles read the same file. Two indexed fields produce 1% and 10% equality
// matches. The full canonical page is the oracle before either path is timed.
func BenchmarkSQLiteIndexedVectorEquality(b *testing.B) {
	ctx := context.Background()
	path := filepath.Join(b.TempDir(), "filter.db")
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64},
		Data:    []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}, {Name: "Hundred", Type: vectordata.TypeInt64, IsIndexed: true}, {Name: "Ten", Type: vectordata.TypeInt64, IsIndexed: true}},
		Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 128, Metric: vectordata.DistanceL1}}}
	_, projected := openVectorSearchFixture(b, path, d, true)
	_, canonical := openVectorSearchFixture(b, path, d, false)
	body := strings.Repeat("x", 4096)
	for start := 0; start < 100000; start += 250 {
		rows := make([]vectordata.ModelRecord, 250)
		for j := range rows {
			i := start + j
			vector := make([]float32, 128)
			for k := range vector {
				vector[k] = float32((i + k) % 127)
			}
			rows[j] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Body": body, "Hundred": int64(i % 100), "Ten": int64(i % 10)}, Vectors: map[string][]float32{"V": vector}}
		}
		if _, err := projected.Insert(ctx, rows); err != nil {
			b.Fatal(err)
		}
	}
	query := make([]float32, 128)
	for _, tc := range []struct {
		field   string
		percent int
	}{{"Hundred", 1}, {"Ten", 10}} {
		opts := vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.Column(tc.field), int64(0))}
		want, err := canonical.Search(ctx, query, 10, opts)
		if err != nil || len(want) != 10 {
			b.Fatalf("canonical page: hits=%d err=%v", len(want), err)
		}
		got, err := projected.Search(ctx, query, 10, opts)
		if err != nil || !reflect.DeepEqual(got, want) {
			b.Fatalf("indexed page differs: %v", err)
		}
		for _, mode := range []struct {
			name string
			c    *Collection
		}{{"canonical", canonical}, {"indexed", projected}} {
			b.Run(fmt.Sprintf("%dpct/%s", tc.percent, mode.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := mode.c.Search(ctx, query, 10, opts); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
