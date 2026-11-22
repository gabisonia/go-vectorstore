package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func lookupFixture(t *testing.T, d vectordata.CollectionDefinition) (*Store, *Collection) {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "lookup.db"), StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c, err := s.EnsureModelCollection(context.Background(), `records"; --`, d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s, c.(*Collection)
}
func TestGetManyBatchesAndOwnership(t *testing.T) {
	ctx := context.Background()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString}, {Name: "Tags", Type: vectordata.TypeStringArray}, {Name: "JSON", Type: vectordata.TypeJSON}}, Vectors: []vectordata.VectorProperty{{Name: "Dense", Dimensions: 2, Optional: true}, {Name: "Bits", Kind: vectordata.VectorBinary, Dimensions: 2, Optional: true}, {Name: "Sparse", Kind: vectordata.VectorSparse, Dimensions: 2, Optional: true}}}
	store, c := lookupFixture(t, d)
	size := 2*lookupBatchSize + 3
	input := make([]vectordata.ModelRecord, size)
	keys := make([]any, 0, size+3)
	bits, _ := vectordata.ParseBinaryVector("10")
	sparse, _ := vectordata.NewSparseVector(2, []int{0}, []float32{1})
	for i := range input {
		input[i] = vectordata.ModelRecord{Key: int64(i), Data: map[string]any{"Title": "kept", "Tags": []string{"one"}, "JSON": map[string]any{"number": json.Number("9007199254740993")}}}
		if i == 0 {
			input[i].Vectors = map[string][]float32{"Dense": {1, 0}}
			input[i].BinaryVectors = map[string]vectordata.BinaryVector{"Bits": bits}
			input[i].SparseVectors = map[string]vectordata.SparseVector{"Sparse": sparse}
		}
		keys = append(keys, int64(i))
	}
	keys = append(keys, int64(-1), int64(0), int64(size-1))
	if _, err := c.Insert(ctx, input); err != nil {
		t.Fatal(err)
	}
	other, err := store.EnsureModelCollection(ctx, "other", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	alien := input[0]
	alien.Data = map[string]any{"Title": "wrong collection", "Tags": []string{}, "JSON": vectordata.JSONNull{}}
	if _, err := other.Insert(ctx, []vectordata.ModelRecord{alien}); err != nil {
		t.Fatal(err)
	}
	for _, include := range []bool{false, true} {
		rows, err := c.GetMany(ctx, keys, vectordata.RetrievalOptions{IncludeVectors: include})
		if err != nil || len(rows) != size {
			t.Fatalf("batched lookup: count=%d %v", len(rows), err)
		}
		seen := map[any]bool{}
		for _, row := range rows {
			if seen[row.Key] {
				t.Fatalf("duplicate result: %v", row.Key)
			}
			seen[row.Key] = true
			if row.Data["Title"] != "kept" || row.Data["JSON"].(map[string]any)["number"] != json.Number("9007199254740993") {
				t.Fatalf("data changed: %+v", row)
			}
			if !include && (row.Vectors != nil || row.BinaryVectors != nil || row.SparseVectors != nil) {
				t.Fatalf("default vector projection: %+v", row)
			}
			if row.Key == int64(0) {
				row.Data["Tags"].([]string)[0] = "changed"
				row.Data["JSON"].(map[string]any)["number"] = 0
				if include {
					if !reflect.DeepEqual(row.Vectors["Dense"], []float32{1, 0}) || !reflect.DeepEqual(row.BinaryVectors["Bits"], bits) || !reflect.DeepEqual(row.SparseVectors["Sparse"], sparse) {
						t.Fatalf("vector projection changed: %+v", row)
					}
					row.Vectors["Dense"][0] = 9
					row.BinaryVectors["Bits"].Bytes[0] = 0
					row.SparseVectors["Sparse"].Values[0] = 9
				}
			}
		}
	}
	again, err := c.GetMany(ctx, []any{int64(0)}, vectordata.RetrievalOptions{IncludeVectors: true})
	if err != nil || len(again) != 1 {
		t.Fatal(err)
	}
	row := again[0]
	if row.Data["Tags"].([]string)[0] != "one" || row.Data["JSON"].(map[string]any)["number"] != json.Number("9007199254740993") || row.Vectors["Dense"][0] != 1 || row.BinaryVectors["Bits"].Bytes[0] == 0 || row.SparseVectors["Sparse"].Values[0] != 1 {
		t.Fatalf("read aliases storage: %+v", row)
	}
	f := vectordata.In(vectordata.Column("ID"), append(keys, nil)...)
	if n, err := c.Count(ctx, f); err != nil || n != int64(size) {
		t.Fatalf("batched key count with duplicates: %d %v", n, err)
	}
	for _, include := range []bool{false, true} {
		selected, err := c.Query(ctx, f, size, vectordata.QueryOptions{IncludeVectors: include})
		if err != nil || len(selected) != size || selected[0].Key != int64(0) {
			t.Fatalf("batched key query: %d %v", len(selected), err)
		}
		first := selected[0]
		if first.Data["JSON"].(map[string]any)["number"] != json.Number("9007199254740993") {
			t.Fatal("query lost JSON number precision")
		}
		first.Data["Tags"].([]string)[0] = "changed"
		first.Data["JSON"].(map[string]any)["number"] = 0
		if include {
			if !reflect.DeepEqual(first.Vectors["Dense"], []float32{1, 0}) || !reflect.DeepEqual(first.BinaryVectors["Bits"], bits) || !reflect.DeepEqual(first.SparseVectors["Sparse"], sparse) {
				t.Fatalf("query vector projection: %+v", first)
			}
			first.Vectors["Dense"][0] = 9
			first.BinaryVectors["Bits"].Bytes[0] = 0
			first.SparseVectors["Sparse"].Values[0] = 9
		} else if first.Vectors != nil || first.BinaryVectors != nil || first.SparseVectors != nil {
			t.Fatal("query default included vectors")
		}
	}
	selected, err := c.Query(ctx, vectordata.Eq(vectordata.Column("ID"), int64(0)), 1, vectordata.QueryOptions{IncludeVectors: true})
	if err != nil || len(selected) != 1 {
		t.Fatal(err)
	}
	row = selected[0]
	if row.Data["Tags"].([]string)[0] != "one" || row.Data["JSON"].(map[string]any)["number"] != json.Number("9007199254740993") || row.Vectors["Dense"][0] != 1 || row.BinaryVectors["Bits"].Bytes[0] == 0 || row.SparseVectors["Sparse"].Values[0] != 1 {
		t.Fatalf("query aliases storage: %+v", row)
	}
	if n, err := c.Count(ctx, nil); err != nil || n != int64(size) {
		t.Fatalf("unfiltered count: %d %v", n, err)
	}
	if n, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Title"), "kept")); err != nil || n != int64(size) {
		t.Fatalf("filtered count fallback: %d %v", n, err)
	}
}

func TestLookupNativeKeys(t *testing.T) {
	for _, tt := range []struct {
		kind          vectordata.KeyType
		stored, alias any
	}{
		{vectordata.KeyString, `quote"; -- / 雪`, `quote"; -- / 雪`},
		{vectordata.KeyInt64, int64(9007199254740993), uint64(9007199254740993)},
		{vectordata.KeyUUID, "8f516db6-38ca-4e8f-ad79-d71b015f5419", "8F516DB6-38CA-4E8F-AD79-D71B015F5419"},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			_, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: tt.kind}})
			if _, err := c.Insert(context.Background(), []vectordata.ModelRecord{{Key: tt.stored}}); err != nil {
				t.Fatal(err)
			}
			rows, err := c.GetMany(context.Background(), []any{tt.stored, tt.alias}, vectordata.RetrievalOptions{})
			if err != nil || len(rows) != 1 || rows[0].Key != tt.stored {
				t.Fatalf("native normalization/dedup: %+v %v", rows, err)
			}
		})
	}
}

func TestLookupsOnlyDecodeRequestedRecords(t *testing.T) {
	ctx := context.Background()
	s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}})
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "wanted"}, {Key: "unrelated"}}); err != nil {
		t.Fatal(err)
	}
	// Deliberately damage an unrelated payload. Indexed lookup and row counting
	// must not deserialize it; requesting that row must still report the damage.
	if _, err := s.db.ExecContext(ctx, `UPDATE __vectorstore_records SET payload=? WHERE collection=? AND key=?`, []byte("invalid JSON"), c.name, keyToken("unrelated")); err != nil {
		t.Fatal(err)
	}
	rows, err := c.GetMany(ctx, []any{"wanted", "missing"}, vectordata.RetrievalOptions{})
	if err != nil || len(rows) != 1 || rows[0].Key != "wanted" {
		t.Fatalf("unrelated row was decoded: %+v %v", rows, err)
	}
	if n, err := c.Count(ctx, nil); err != nil || n != 2 {
		t.Fatalf("count decoded payloads: %d %v", n, err)
	}
	if rows, err := c.GetMany(ctx, []any{"wanted", "unrelated"}, vectordata.RetrievalOptions{}); err == nil || rows != nil {
		t.Fatalf("partial results escaped: %+v %v", rows, err)
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "after error"}}); err != nil {
		t.Fatalf("read transaction leaked: %v", err)
	}
}

func TestLookupMetadataAndCancellation(t *testing.T) {
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
		if _, err := tt.c.GetMany(ctx, []any{"not found"}, vectordata.RetrievalOptions{}); !errors.Is(err, tt.want) {
			t.Fatalf("metadata bypassed for empty matches: %v", err)
		}
		if _, err := tt.c.Count(ctx, nil); !errors.Is(err, tt.want) {
			t.Fatalf("metadata bypassed for count: %v", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.GetMany(canceled, []any{"x"}, vectordata.RetrievalOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("lookup cancellation: %v", err)
	}
	if _, err := c.Count(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("count cancellation: %v", err)
	}
	if _, err := c.GetMany(ctx, []any{123}, vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("invalid key: %v", err)
	}
	if n, err := c.Count(ctx, nil); err != nil || n != 0 {
		t.Fatalf("empty collection count: %d %v", n, err)
	}
	if rows, err := missing.GetMany(ctx, nil, vectordata.RetrievalOptions{}); err != nil || len(rows) != 0 {
		t.Fatalf("empty lookup semantics: %+v %v", rows, err)
	}
}

func TestInvalidUTF8KeysDoNotAliasStoredKeys(t *testing.T) {
	ctx := context.Background()
	_, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}})
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "\ufffd"}, {Key: "other"}}); err != nil {
		t.Fatal(err)
	}
	bad := string([]byte{0xff})
	for _, run := range []func() error{
		func() error { _, err := c.Get(ctx, bad, vectordata.RetrievalOptions{}); return err },
		func() error { _, err := c.GetMany(ctx, []any{bad}, vectordata.RetrievalOptions{}); return err },
		func() error { _, err := c.Delete(ctx, []any{"other", bad}); return err },
	} {
		if err := run(); !errors.Is(err, vectordata.ErrInvalidRecord) {
			t.Fatalf("invalid UTF-8 key accepted: %v", err)
		}
	}
	if n, err := c.Count(ctx, nil); err != nil || n != 2 {
		t.Fatalf("invalid key deleted valid records: %d %v", n, err)
	}
}

func TestLookupAndCountIndexPlans(t *testing.T) {
	ctx := context.Background()
	s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}})
	for _, tt := range []struct {
		query      string
		args       []any
		constraint string
	}{
		{lookupSQL(2), []any{c.name, keyToken("one"), keyToken("two")}, "key=?"},
		{countKeysSQL(2), []any{c.name, keyToken("one"), keyToken("two")}, "COVERING INDEX"},
		{countKeysSQL(2), []any{c.name, keyToken("one"), keyToken("two")}, "key=?"},
		{countAllSQL, []any{c.name}, "COVERING INDEX"},
	} {
		rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+tt.query, tt.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plans []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plans = append(plans, detail)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		used := false
		for _, plan := range plans {
			if strings.Contains(plan, "SEARCH") && strings.Contains(plan, "__vectorstore_records") && strings.Contains(plan, tt.constraint) {
				used = true
			}
		}
		if !used {
			t.Fatalf("requested index path absent: %s", fmt.Sprint(plans))
		}
	}
}

func TestPointAndBatchReadsValidateSelectedVectors(t *testing.T) {
	ctx := context.Background()
	s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}})
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "bad", Vectors: map[string][]float32{"Vector": {1, 0}}}}); err != nil {
		t.Fatal(err)
	}
	payload, err := encodeRecord(vectordata.ModelRecord{Key: "bad", Data: map[string]any{}, Vectors: map[string][]float32{"Vector": {1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE __vectorstore_records SET payload=? WHERE collection=? AND key=?`, payload, c.name, keyToken("bad")); err != nil {
		t.Fatal(err)
	}
	for _, include := range []bool{false, true} {
		opts := vectordata.RetrievalOptions{IncludeVectors: include}
		if _, err := c.Get(ctx, "bad", opts); !errors.Is(err, vectordata.ErrDimensionMismatch) {
			t.Fatalf("Get(include=%v): %v", include, err)
		}
		if _, err := c.GetMany(ctx, []any{"bad"}, opts); !errors.Is(err, vectordata.ErrDimensionMismatch) {
			t.Fatalf("GetMany(include=%v): %v", include, err)
		}
	}
}

func TestLookupRejectsMismatchedStoredKey(t *testing.T) {
	ctx := t.Context()
	s, c := lookupFixture(t, vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}})
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "a"}, {Key: "b"}}); err != nil {
		t.Fatal(err)
	}
	// Both keys exist in the requested batch. Membership alone cannot establish
	// that each payload belongs to its own physical row.
	if _, err := s.db.ExecContext(ctx, `UPDATE __vectorstore_records SET payload=(SELECT payload FROM __vectorstore_records WHERE collection=? AND key=?) WHERE collection=? AND key=?`, c.name, keyToken("b"), c.name, keyToken("a")); err != nil {
		t.Fatal(err)
	}
	for _, include := range []bool{false, true} {
		opts := vectordata.RetrievalOptions{IncludeVectors: include}
		if got, err := c.Get(ctx, "a", opts); !errors.Is(err, vectordata.ErrInvalidRecord) || got.Key != nil {
			t.Fatalf("Get: %v, %v", got, err)
		}
		if got, err := c.GetMany(ctx, []any{"a", "b"}, opts); !errors.Is(err, vectordata.ErrInvalidRecord) || got != nil {
			t.Fatalf("GetMany: %v, %v", got, err)
		}
	}
}

func TestStoredKeyZeroRequiresAnExplicitNumericKey(t *testing.T) {
	for _, kind := range []vectordata.KeyType{vectordata.KeyInt64, vectordata.KeyUint64} {
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: kind}}
		for _, payload := range []string{`{}`, `{"Key": null}`} {
			if _, err := decodeStoredRecord([]byte(payload), "0", d); !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("%s %s: %v", kind, payload, err)
			}
		}
		if _, err := decodeStoredRecord([]byte(`{"Key":0}`), "0", d); err != nil {
			t.Fatalf("%s zero: %v", kind, err)
		}
	}
}
