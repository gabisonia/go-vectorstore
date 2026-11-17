package sqlite

import (
	"context"
	"database/sql"
	"math"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestNativeScalarIndexesUseOneWALSnapshot(t *testing.T) {
	for _, operation := range []string{"query", "count"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "snapshot.db")
			store, err := Open(ctx, path, StoreOptions{NativeIndexes: true})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			writer, err := Open(ctx, path, StoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			base, err := store.EnsureModelCollection(ctx, "records", scalarIndexDefinition(), vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			c := base.(*Collection)
			other, err := writer.GetModelCollection(c.name, c.Definition(), vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{scalarIndexRow(int64(1), 1)}); err != nil {
				t.Fatal(err)
			}
			changed := false
			connector := lookupConnector{base: store.db.Driver(), dsn: path + "?_txlock=immediate&_pragma=busy_timeout(0)", before: func(query string) {
				if changed || !strings.HasPrefix(query, "SELECT definition,properties,version") {
					return
				}
				changed = true
				deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				if _, err := other.Upsert(deadline, []vectordata.ModelRecord{scalarIndexRow(int64(1), 2)}); err != nil {
					t.Errorf("WAL writer blocked: %v", err)
				}
			}}
			if err := store.db.Close(); err != nil {
				t.Fatal(err)
			}
			store.db = sql.OpenDB(connector)
			store.db.SetMaxOpenConns(1)
			f := vectordata.Eq(vectordata.Column("Number"), int64(1))
			if operation == "query" {
				rows, err := c.Query(ctx, f, 2, vectordata.QueryOptions{})
				if err != nil || len(rows) != 1 || rows[0].Data["Number"] != int64(1) {
					t.Fatalf("mixed query snapshot: %v %v", rows, err)
				}
			} else if n, err := c.Count(ctx, f); err != nil || n != 1 {
				t.Fatalf("mixed count snapshot: %d %v", n, err)
			}
			if !changed {
				t.Fatal("did not interleave a publication")
			}
			if n, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Number"), int64(2))); err != nil || n != 1 {
				t.Fatalf("next snapshot: %d %v", n, err)
			}
		})
	}
}

func TestNativeScalarIndexFloatRoundTrips(t *testing.T) {
	ctx := context.Background()
	_, c := scalarIndexFixture(t, scalarIndexDefinition(), true)
	rng := rand.New(rand.NewSource(20261117))
	want := map[float64]int64{}
	var input []vectordata.ModelRecord
	for range 256 {
		value := math.Float64frombits(rng.Uint64())
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		r := scalarIndexRow(nil, 1)
		r.Data["Amount"] = value
		input = append(input, r)
		want[value]++
	}
	if _, err := c.Insert(ctx, input); err != nil {
		t.Fatal(err)
	}
	for value, n := range want {
		if got, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Amount"), value)); err != nil || got != n {
			t.Fatalf("float %g: %d %v want %d", value, got, err, n)
		}
	}
}

func TestNativeScalarIndexEnsureFailureRollsBackRegistration(t *testing.T) {
	ctx := context.Background()
	s, c := scalarIndexFixture(t, scalarIndexDefinition(), true)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_index_registration BEFORE INSERT ON __vectorstore_scalar_indexes WHEN NEW.collection='new' BEGIN SELECT RAISE(ABORT,'injected registration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureModelCollection(ctx, "new", c.Definition(), vectordata.ModelCollectionOptions{}); err == nil {
		t.Fatal("ensure should fail")
	}
	if exists, err := s.CollectionExists(ctx, "new"); err != nil || exists {
		t.Fatalf("partial collection: %v %v", exists, err)
	}
}

func TestNativeScalarIndexBooleanFilterParity(t *testing.T) {
	ctx := context.Background()
	_, c := scalarIndexFixture(t, scalarIndexDefinition(), true)
	var input []vectordata.ModelRecord
	for i := range 12 {
		r := scalarIndexRow(int64(i), int64(i%4))
		if i%3 == 0 {
			r.Data["Number"] = nil
		}
		if i%4 == 0 {
			r.Data["Active"] = nil
		} else {
			r.Data["Active"] = i%2 == 0
		}
		r.Data["Unindexed"] = "x"
		input = append(input, r)
	}
	if _, err := c.Insert(ctx, input); err != nil {
		t.Fatal(err)
	}
	reference, err := c.engine(ctx)
	if err != nil {
		t.Fatal(err)
	}
	leaves := []vectordata.Filter{
		vectordata.Eq(vectordata.Column("Number"), nil), vectordata.Gt(vectordata.Column("Number"), int64(1)),
		vectordata.In(vectordata.Column("Number"), int64(1), nil), vectordata.Exists(vectordata.Column("Number")),
		vectordata.Eq(vectordata.Column("Active"), false), vectordata.Eq(vectordata.Column("Active"), nil),
		vectordata.Eq(vectordata.Column("Unindexed"), "x"), vectordata.Eq(vectordata.Column("Unindexed"), "absent"),
	}
	rng := rand.New(rand.NewSource(20261118))
	var generate func(int) vectordata.Filter
	generate = func(depth int) vectordata.Filter {
		if depth == 0 || rng.Intn(4) == 0 {
			return leaves[rng.Intn(len(leaves))]
		}
		switch rng.Intn(3) {
		case 0:
			return vectordata.Not(generate(depth - 1))
		case 1:
			return vectordata.And(generate(depth-1), generate(depth-1))
		default:
			return vectordata.Or(generate(depth-1), generate(depth-1))
		}
	}
	for range 128 {
		filter := generate(4)
		got, err := c.Count(ctx, filter)
		want, wantErr := reference.Count(ctx, filter)
		if err != nil || wantErr != nil || got != want {
			t.Fatalf("count %+v: %d/%v want %d/%v", filter, got, err, want, wantErr)
		}
		opts := vectordata.QueryOptions{Skip: 1, OrderBy: []vectordata.SortField{{Property: "Number"}}}
		rows, err := c.Query(ctx, filter, 4, opts)
		expected, wantErr := reference.Query(ctx, filter, 4, opts)
		if err != nil || wantErr != nil || !reflect.DeepEqual(rows, expected) {
			t.Fatalf("query %+v: %v/%v want %v/%v", filter, rows, err, expected, wantErr)
		}
	}
}
