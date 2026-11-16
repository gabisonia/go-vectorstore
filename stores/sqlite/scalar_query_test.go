package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

const scalarTextName = `text."[]'; -- 雪`

func TestScalarFloat64EqualityKeepsRoundTripPrecision(t *testing.T) {
	ctx := context.Background()
	_, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Amount", Type: vectordata.TypeFloat64}}})
	rng := rand.New(rand.NewSource(20261116))
	var rows []vectordata.ModelRecord
	want := map[float64]int64{}
	for range 256 {
		value := math.Float64frombits(rng.Uint64())
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		rows = append(rows, vectordata.ModelRecord{Key: int64(len(rows)), Data: map[string]any{"Amount": value}})
		want[value]++
	}
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	for value, count := range want {
		got, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Amount"), value))
		if err != nil || got != count {
			t.Fatalf("float64 %g: got %d/%v, want %d", value, got, err, count)
		}
	}
	values := make([]any, 0, len(rows))
	for value := range want {
		values = append(values, value)
	}
	if got, err := c.Count(ctx, vectordata.In(vectordata.Column("Amount"), values...)); err != nil || got != int64(len(rows)) {
		t.Fatalf("float64 In: %d %v", got, err)
	}
}

func TestScalarQueryValidatesOnlyReturnedPageAndDiscardsPartialResults(t *testing.T) {
	ctx := context.Background()
	s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Number", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}})
	rows := []vectordata.ModelRecord{{Key: "a", Data: map[string]any{"Number": int64(0)}, Vectors: map[string][]float32{"Vector": {1, 0}}}, {Key: "b", Data: map[string]any{"Number": int64(1)}, Vectors: map[string][]float32{"Vector": {0, 1}}}}
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	bad := rows[1]
	bad.Vectors = map[string][]float32{"Vector": {1}}
	payload, err := encodeRecord(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE __vectorstore_records SET payload=? WHERE collection=? AND key=?`, payload, c.name, keyToken("b")); err != nil {
		t.Fatal(err)
	}
	f := vectordata.Gte(vectordata.Column("Number"), int64(0))
	page, err := c.Query(ctx, f, 1, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "Number"}}})
	if err != nil || len(page) != 1 || page[0].Key != "a" {
		t.Fatalf("decoded outside returned page: %+v %v", page, err)
	}
	if count, err := c.Count(ctx, f); err != nil || count != 2 {
		t.Fatalf("scalar count decoded vectors: %d %v", count, err)
	}
	seen := 0
	for _, err := range c.QueryIter(ctx, f, 2, vectordata.QueryOptions{}) {
		if !errors.Is(err, vectordata.ErrDimensionMismatch) {
			t.Fatalf("invalid selected row: %v", err)
		}
		seen++
	}
	if seen != 1 {
		t.Fatalf("partial rows escaped: %d", seen)
	}
	if _, err := c.Upsert(ctx, rows); err != nil {
		t.Fatalf("query error leaked connection: %v", err)
	}
}

func TestScalarQueriesMatchSnapshotEvaluation(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		kind vectordata.KeyType
		keys []any
	}{
		{vectordata.KeyString, []any{"2", "10", "zero\x00byte", "quote\"", "雪", "\ufffd", "a", "z"}},
		{vectordata.KeyInt64, []any{int64(math.MinInt64), int64(-9007199254740993), int64(-1), int64(0), int64(2), int64(10), int64(9007199254740993), int64(math.MaxInt64)}},
		{vectordata.KeyUint64, []any{uint64(0), uint64(2), uint64(10), uint64(9007199254740992), uint64(9007199254740993), uint64(math.MaxInt64), uint64(math.MaxInt64) + 1, uint64(math.MaxUint64)}},
		{vectordata.KeyUUID, []any{"00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000010", "11111111-1111-1111-1111-111111111111", "8f516db6-38ca-4e8f-ad79-d71b015f5419", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "ffffffff-ffff-ffff-ffff-ffffffffffff"}},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: tt.kind}, Data: []vectordata.DataProperty{
				{Name: scalarTextName, StorageName: "different", Type: vectordata.TypeString, Optional: true},
				{Name: "Number", Type: vectordata.TypeInt64, Optional: true}, {Name: "Amount", Type: vectordata.TypeFloat64, Optional: true}, {Name: "Active", Type: vectordata.TypeBool, Optional: true},
				{Name: "JSON", Type: vectordata.TypeJSON, Optional: true}, {Name: "Decimal", Type: vectordata.TypeDecimal, Optional: true},
			}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Precision: vectordata.VectorFloat16, Optional: true}}}
			_, c := lookupFixture(t, d)
			texts := []any{nil, "2", "10", "a\x00z", "雪", "quote'--", "", "a"}
			numbers := []any{nil, int64(math.MinInt64), int64(-9007199254740993), int64(0), int64(2), int64(10), int64(9007199254740993), int64(math.MaxInt64)}
			amounts := []any{nil, -math.MaxFloat64, -1.0, 0.0, math.SmallestNonzeroFloat64, 1.0, math.Nextafter(1, 2), math.MaxFloat64}
			records := make([]vectordata.ModelRecord, len(tt.keys))
			for i, key := range tt.keys {
				var active any = i%2 == 0
				if i%3 == 0 {
					active = nil
				}
				records[i] = vectordata.ModelRecord{Key: key, Data: map[string]any{scalarTextName: texts[i], "Number": numbers[i], "Amount": amounts[i], "Active": active, "JSON": map[string]any{"n": json.Number("9007199254740993")}, "Decimal": "0.1000000000000000000000000001"}, Vectors: map[string][]float32{"Vector": {float32(i) / 3, 1}}}
			}
			if _, err := c.Insert(ctx, records); err != nil {
				t.Fatal(err)
			}
			reference, err := c.engine(ctx)
			if err != nil {
				t.Fatal(err)
			}
			filters := []vectordata.Filter{
				nil, vectordata.Eq(vectordata.Column(scalarTextName), "a\x00z"), vectordata.In(vectordata.Column(scalarTextName), "2", "雪", nil),
				vectordata.Not(vectordata.Eq(vectordata.Column(scalarTextName), "2")), vectordata.Not(vectordata.In(vectordata.Column(scalarTextName), "2", nil)),
				vectordata.Eq(vectordata.Column("Number"), int64(9007199254740993)), vectordata.In(vectordata.Column("Number"), nil, int64(0), uint64(math.MaxInt64)),
				vectordata.Gte(vectordata.Column("Number"), int64(-9007199254740993)), vectordata.Lte(vectordata.Column("Number"), int64(math.MaxInt64)),
				vectordata.Gt(vectordata.Column("Number"), int64(0)), vectordata.Lt(vectordata.Column("Number"), int64(0)),
				vectordata.Eq(vectordata.Column("Amount"), math.SmallestNonzeroFloat64), vectordata.Eq(vectordata.Column("Amount"), 0.0), vectordata.Gt(vectordata.Column("Amount"), 1.0),
				vectordata.Eq(vectordata.Column("Active"), false), vectordata.Eq(vectordata.Column("Active"), nil), vectordata.Exists(vectordata.Column("Active")),
				vectordata.Not(vectordata.Eq(vectordata.Column("Active"), true)), vectordata.In(vectordata.Column("Active"), true, nil),
				vectordata.And(vectordata.Gte(vectordata.Column("Number"), int64(0)), vectordata.Or(vectordata.Eq(vectordata.Column("Active"), false), vectordata.Eq(vectordata.Column(scalarTextName), "a"))),
				vectordata.Not(vectordata.In(vectordata.Column("ID"), tt.keys[0], nil)), vectordata.And(vectordata.Exists(vectordata.Column("ID")), vectordata.Gt(vectordata.Column("ID"), tt.keys[2])),
				vectordata.Or(vectordata.Eq(vectordata.Column("ID"), nil), vectordata.Lte(vectordata.Column("ID"), tt.keys[4])),
				vectordata.Eq(vectordata.DataPath("JSON", "n"), json.Number("9007199254740993")), vectordata.Eq(vectordata.Column("Decimal"), "0.1000000000000000000000000001"),
				vectordata.And(vectordata.Eq(vectordata.Column("Number"), int64(0)), vectordata.Eq(vectordata.DataPath("JSON", "n"), json.Number("9007199254740993"))),
				vectordata.Not(vectordata.Eq(vectordata.Column(scalarTextName), string([]byte{0xff}))),
			}
			for index, filter := range filters {
				got, err := c.Count(ctx, filter)
				want, wantErr := reference.Count(ctx, filter)
				if err != nil || wantErr != nil || got != want {
					t.Fatalf("count case %d: got %d/%v, want %d/%v", index, got, err, want, wantErr)
				}
				for _, opts := range []vectordata.QueryOptions{
					{}, {OrderBy: []vectordata.SortField{{Property: "ID", Descending: true}}, Skip: 1, IncludeVectors: true},
					{OrderBy: []vectordata.SortField{{Property: "Number"}}}, {OrderBy: []vectordata.SortField{{Property: "Number", Descending: true}, {Property: scalarTextName}}, Skip: 1},
					{OrderBy: []vectordata.SortField{{Property: "Amount"}}, IncludeVectors: true}, {OrderBy: []vectordata.SortField{{Property: "Active", Descending: true}}},
					{OrderBy: []vectordata.SortField{{Property: scalarTextName}}, Skip: 2}, {OrderBy: []vectordata.SortField{{Property: "Decimal"}}}, {Skip: math.MaxInt},
				} {
					got, err := c.Query(ctx, filter, 3, opts)
					want, wantErr := reference.Query(ctx, filter, 3, opts)
					if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("query case %d / %+v: got %+v/%v, want %+v/%v", index, opts, got, err, want, wantErr)
					}
				}
			}
		})
	}
}

func TestScalarQueryPlansBoundNamesAndValuesAndFallback(t *testing.T) {
	ctx := context.Background()
	_, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: scalarTextName, Type: vectordata.TypeString}, {Name: "JSON", Type: vectordata.TypeJSON}, {Name: "Decimal", Type: vectordata.TypeDecimal}}})
	filter := vectordata.Eq(vectordata.Column(scalarTextName), "x'); DROP TABLE records; --")
	plan, ok, err := c.scalarPlan(filter, 2, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: scalarTextName}}, Skip: 1}, false)
	if err != nil || !ok || strings.Contains(plan.sql, scalarTextName) || strings.Contains(plan.sql, "DROP TABLE") {
		t.Fatalf("unbound plan: %+v %t %v", plan, ok, err)
	}
	if _, err := c.Query(ctx, filter, 2, vectordata.QueryOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []vectordata.Filter{vectordata.Eq(vectordata.DataPath("JSON", "path"), 1), vectordata.Eq(vectordata.Column("Decimal"), "1"), vectordata.Eq(vectordata.Column(scalarTextName), string([]byte{0xff}))} {
		if _, ok, err := c.scalarPlan(f, 1, vectordata.QueryOptions{}, false); err != nil || ok {
			t.Fatalf("unsupported filter did not fall back: %T %t %v", f, ok, err)
		}
	}
	deep := filter
	for range 30 {
		deep = vectordata.Not(deep)
	}
	if _, ok, err := c.scalarPlan(deep, 1, vectordata.QueryOptions{}, false); err != nil || ok {
		t.Fatal("deep expression did not fall back")
	}
	if _, ok, err := c.scalarPlan(filter, 1, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "Decimal"}}}, false); err != nil || ok {
		t.Fatal("unsupported sort did not fall back")
	}
	values := make([]any, 1200)
	for i := range values {
		values[i] = "value"
	}
	if _, err := c.Count(ctx, vectordata.In(vectordata.Column(scalarTextName), values...)); err != nil {
		t.Fatalf("large In exceeded parameter limit: %v", err)
	}
}

func TestScalarEmptyMatchesStillValidateRegistration(t *testing.T) {
	ctx := context.Background()
	s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString}}})
	d := c.Definition()
	d.Data = append(d.Data, vectordata.DataProperty{Name: "New", Type: vectordata.TypeString, Optional: true})
	stale, err := s.GetModelCollection(c.name, d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := s.GetModelCollection("absent", c.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		c    vectordata.ModelCollection
		want error
	}{{stale, vectordata.ErrSchemaMismatch}, {missing, vectordata.ErrCollectionNotFound}} {
		f := vectordata.Eq(vectordata.Column("Title"), "absent")
		if _, err := tt.c.Query(ctx, f, 1, vectordata.QueryOptions{Skip: 100}); !errors.Is(err, tt.want) {
			t.Fatalf("query metadata: %v", err)
		}
		if _, err := tt.c.Count(ctx, f); !errors.Is(err, tt.want) {
			t.Fatalf("count metadata: %v", err)
		}
	}
}
