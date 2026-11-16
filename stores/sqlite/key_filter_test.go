package sqlite

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type keyFilterUint uint64

type keyFilterUUID struct {
	text  string
	check func() error
}

func (k keyFilterUUID) MarshalText() ([]byte, error) {
	if err := k.check(); err != nil {
		return nil, err
	}
	return []byte(k.text), nil
}

func TestKeyFilterNormalizationCanReenterStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyUUID}})
	uuid := "8f516db6-38ca-4e8f-ad79-d71b015f5419"
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: uuid}}); err != nil {
		t.Fatal(err)
	}
	key := keyFilterUUID{text: uuid, check: func() error { _, err := c.Count(ctx, nil); return err }}
	for _, f := range []vectordata.Filter{vectordata.Eq(vectordata.Column("ID"), key), vectordata.In(vectordata.Column("ID"), key, key)} {
		if count, err := c.Count(ctx, f); err != nil || count != 1 {
			t.Fatalf("count normalized key while holding connection: %d %v", count, err)
		}
		if rows, err := c.Query(ctx, f, 1, vectordata.QueryOptions{}); err != nil || len(rows) != 1 {
			t.Fatalf("query normalized key while holding connection: %+v %v", rows, err)
		}
	}
}

func TestKeyFilterNormalizationErrorsRemainFilterErrors(t *testing.T) {
	ctx := context.Background()
	_, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyUUID}})
	for _, op := range []string{"query", "count"} {
		calls := 0
		key := keyFilterUUID{text: "8f516db6-38ca-4e8f-ad79-d71b015f5419", check: func() error {
			calls++
			if calls > 1 {
				return errors.New("key mapping failed")
			}
			return nil
		}}
		filter := vectordata.Eq(vectordata.Column("ID"), key)
		var err error
		if op == "query" {
			_, err = c.Query(ctx, filter, 1, vectordata.QueryOptions{})
		} else {
			_, err = c.Count(ctx, filter)
		}
		if !errors.Is(err, vectordata.ErrInvalidFilter) {
			t.Fatalf("%s lost operand error category: %v", op, err)
		}
	}
}

func TestKeyFiltersMatchSnapshotEvaluation(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		kind           vectordata.KeyType
		keys           []any
		alias, missing any
	}{
		{vectordata.KeyString, []any{"\ufffd", `quote"; -- / 雪`, "2", "10", "zero\x00byte"}, "zero\x00byte", "missing"},
		{vectordata.KeyInt64, []any{int64(math.MinInt64), int64(-9007199254740993), int64(0), int64(2), int64(10), int64(math.MaxInt64)}, uint64(math.MaxInt64), int64(42)},
		{vectordata.KeyUint64, []any{uint64(0), uint64(2), uint64(10), uint64(9007199254740993), uint64(math.MaxInt64) + 1, uint64(math.MaxUint64)}, keyFilterUint(math.MaxUint64), uint64(42)},
		{vectordata.KeyUUID, []any{"00000000-0000-0000-0000-000000000000", "8f516db6-38ca-4e8f-ad79-d71b015f5419", "ffffffff-ffff-ffff-ffff-ffffffffffff"}, strings.ToUpper("ffffffff-ffff-ffff-ffff-ffffffffffff"), "11111111-1111-1111-1111-111111111111"},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			_, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", StorageName: "stored_id", Type: tt.kind}, Data: []vectordata.DataProperty{{Name: "Rank", Type: vectordata.TypeInt64, Optional: true}, {Name: "Title", Type: vectordata.TypeString}}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, Metric: vectordata.DistanceL2, Optional: true}}})
			rows := make([]vectordata.ModelRecord, len(tt.keys))
			for i, key := range tt.keys {
				var rank any = int64(i % 2)
				if i == len(tt.keys)-1 {
					rank = nil
				}
				rows[i] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Rank": rank, "Title": "title"}, Vectors: map[string][]float32{"Vector": {float32(i), 1}}}
			}
			if _, err := c.Insert(ctx, rows); err != nil {
				t.Fatal(err)
			}
			reference, err := c.engine(ctx)
			if err != nil {
				t.Fatal(err)
			}
			values := append(append([]any{}, tt.keys...), tt.alias, tt.missing, nil, tt.keys[0])
			filters := []vectordata.Filter{
				vectordata.Eq(vectordata.Column(" ID "), tt.alias), vectordata.In(vectordata.Column("ID"), values...),
				vectordata.Eq(vectordata.Column("ID"), nil), vectordata.In(vectordata.Column("ID"), nil),
				vectordata.Eq(vectordata.Column("ID"), tt.missing),
				vectordata.In(vectordata.Column("Title"), "title"),
				vectordata.And(vectordata.In(vectordata.Column("ID"), values...), vectordata.Eq(vectordata.Column("Title"), "title")),
				vectordata.Not(vectordata.Eq(vectordata.Column("ID"), tt.alias)),
			}
			if tt.kind == vectordata.KeyString {
				filters = append(filters, vectordata.Eq(vectordata.Column("ID"), string([]byte{0xff})), vectordata.In(vectordata.Column("ID"), string([]byte{0xff}), tt.keys[0]))
			}
			for _, filter := range filters {
				got, err := c.Count(ctx, filter)
				want, wantErr := reference.Count(ctx, filter)
				if err != nil || wantErr != nil || got != want {
					t.Fatalf("count %v: got %d/%v, want %d/%v", filter, got, err, want, wantErr)
				}
				for _, opts := range []vectordata.QueryOptions{
					{}, {OrderBy: []vectordata.SortField{{Property: "ID", Descending: true}}, IncludeVectors: true},
					{OrderBy: []vectordata.SortField{{Property: "Rank"}}, Skip: 1},
					{OrderBy: []vectordata.SortField{{Property: "Rank", Descending: true}}, IncludeVectors: true}, {Skip: math.MaxInt},
				} {
					got, err := c.Query(ctx, filter, 3, opts)
					want, wantErr := reference.Query(ctx, filter, 3, opts)
					if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("query %v %+v: got %+v/%v, want %+v/%v", filter, opts, got, err, want, wantErr)
					}
				}
			}
		})
	}
}

func TestKeyFiltersValidateMetadataAndOptionsOnEmptyMatches(t *testing.T) {
	ctx := context.Background()
	s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}})
	wrong := c.Definition()
	wrong.Data = []vectordata.DataProperty{{Name: "Extra", Type: vectordata.TypeString, Optional: true}}
	stale, err := s.GetModelCollection(c.name, wrong, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := s.GetModelCollection("missing", c.Definition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		c    vectordata.ModelCollection
		want error
	}{{stale, vectordata.ErrSchemaMismatch}, {missing, vectordata.ErrCollectionNotFound}} {
		for _, f := range []vectordata.Filter{vectordata.Eq(vectordata.Column("ID"), nil), vectordata.In(vectordata.Column("ID"), nil), vectordata.In(vectordata.Column("ID"), "absent")} {
			if _, err := tt.c.Count(ctx, f); !errors.Is(err, tt.want) {
				t.Fatalf("empty count metadata: %v", err)
			}
			if _, err := tt.c.Query(ctx, f, 1, vectordata.QueryOptions{Skip: 100}); !errors.Is(err, tt.want) {
				t.Fatalf("empty query metadata: %v", err)
			}
		}
	}
	// Closed storage proves validation finishes before acquiring a connection.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []vectordata.Filter{vectordata.In(vectordata.Column("ID")), vectordata.In(vectordata.Column("ID"), "valid", 42), vectordata.Eq(vectordata.Column("missing"), "key")} {
		if _, err := c.Count(ctx, f); !errors.Is(err, vectordata.ErrInvalidFilter) {
			t.Fatalf("filter validation: %v", err)
		}
		if _, err := c.Query(ctx, f, 1, vectordata.QueryOptions{}); !errors.Is(err, vectordata.ErrInvalidFilter) {
			t.Fatalf("query filter validation: %v", err)
		}
	}
	f := vectordata.Eq(vectordata.Column("ID"), "absent")
	if _, err := c.Query(ctx, f, 0, vectordata.QueryOptions{}); !errors.Is(err, vectordata.ErrInvalidSearchOptions) {
		t.Fatalf("top validation: %v", err)
	}
	if _, err := c.Query(ctx, f, 1, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: "missing"}}}); !errors.Is(err, vectordata.ErrInvalidFilter) {
		t.Fatalf("sort validation: %v", err)
	}
}

func TestKeyFiltersReadOnlySelectedPayloads(t *testing.T) {
	ctx := context.Background()
	s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}})
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "wanted", Vectors: map[string][]float32{"Vector": {1, 0}}}, {Key: "bad", Vectors: map[string][]float32{"Vector": {0, 1}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE __vectorstore_records SET payload=? WHERE collection=? AND key=?`, []byte("invalid JSON"), c.name, keyToken("bad")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []vectordata.Filter{vectordata.Eq(vectordata.Column("ID"), "wanted"), vectordata.In(vectordata.Column("ID"), "wanted", "missing", "wanted")} {
		rows, err := c.Query(ctx, f, 1, vectordata.QueryOptions{})
		if err != nil || len(rows) != 1 || rows[0].Key != "wanted" {
			t.Fatalf("unrelated payload decoded: %+v %v", rows, err)
		}
	}
	f := vectordata.In(vectordata.Column("ID"), "wanted", "bad", "bad")
	if count, err := c.Count(ctx, f); err != nil || count != 2 {
		t.Fatalf("key count decoded payloads: %d %v", count, err)
	}
	if rows, err := c.Query(ctx, f, 1, vectordata.QueryOptions{}); err == nil || rows != nil {
		t.Fatalf("selected corruption escaped: %+v %v", rows, err)
	}
	if _, err := c.Count(ctx, vectordata.And(f, vectordata.Eq(vectordata.Column("ID"), "wanted"))); err == nil {
		t.Fatal("compound predicate bypassed fallback")
	}
	// A selected record's invalid vector must fail even without vector projection.
	payload, err := encodeRecord(vectordata.ModelRecord{Key: "bad", Vectors: map[string][]float32{"Vector": {1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE __vectorstore_records SET payload=? WHERE collection=? AND key=?`, payload, c.name, keyToken("bad")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(ctx, f, 1, vectordata.QueryOptions{}); !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("selected vector validation: %v", err)
	}
	if _, err := c.Upsert(ctx, []vectordata.ModelRecord{{Key: "after_error", Vectors: map[string][]float32{"Vector": {1, 0}}}}); err != nil {
		t.Fatalf("read transaction leaked: %v", err)
	}
}
