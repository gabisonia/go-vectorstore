package exactsearch

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type filterUUID struct{ text string }

func (u filterUUID) MarshalText() ([]byte, error) { return []byte(u.text), nil }
func TestFrozenFilterSemantics(t *testing.T) {
	uuid := "8f516db6-38ca-4e8f-ad79-d71b015f5419"
	d, _ := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyUUID}, Data: []vectordata.DataProperty{
		{Name: "JSON", Type: vectordata.TypeJSON, Optional: true}, {Name: "Strings", Type: vectordata.TypeStringArray, Optional: true}, {Name: "Ints", Type: vectordata.TypeInt64Array, Optional: true},
		{Name: "UUID", Type: vectordata.TypeUUID, Optional: true}, {Name: "Time", Type: vectordata.TypeTime, Optional: true}, {Name: "Decimal", Type: vectordata.TypeDecimal, Optional: true},
	}}).Normalize()
	instant := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	records := []vectordata.ModelRecord{
		{Key: uuid, Data: map[string]any{"JSON": map[string]any{"n": json.Number("9007199254740993"), "array": []any{nil, []any{json.Number("1")}, "x"}}, "Strings": []string{"x"}, "Ints": []int64{1}, "UUID": uuid, "Time": instant, "Decimal": vectordata.Decimal("0.1")}},
		{Key: uuid, Data: map[string]any{"JSON": vectordata.JSONNull{}, "Strings": []string{}, "Ints": []int64{}}},
		{Key: uuid, Data: map[string]any{}},
	}
	path := vectordata.DataPath("JSON", "array")
	n := vectordata.DataPath("JSON", "n")
	filters := []vectordata.Filter{
		vectordata.Eq(vectordata.Column("ID"), filterUUID{uuid}), vectordata.Eq(vectordata.Column("UUID"), filterUUID{uuid}),
		vectordata.Gte(vectordata.Column("Time"), instant), vectordata.Lte(vectordata.Column("Decimal"), "1e-1"),
		vectordata.Gt(n, json.Number("9007199254740992")), vectordata.Lt(n, "z"),
		vectordata.In(vectordata.Column("JSON"), nil, vectordata.JSONNull{}, json.RawMessage("null")),
		vectordata.Eq(vectordata.Column("JSON"), map[string]any(nil)), vectordata.Eq(n, (*string)(nil)),
		vectordata.Contains(path, json.RawMessage("[1]")), vectordata.ContainsAny(path, vectordata.JSONNull{}, "x"), vectordata.ContainsAll(path, nil, "x"),
		vectordata.ContainsAll(vectordata.Column("Strings")), vectordata.Not(vectordata.ContainsAny(vectordata.Column("Ints"))),
		vectordata.Eq(vectordata.Column("Strings"), []string(nil)), vectordata.In(vectordata.Column("Ints"), []int64{1}, []int64(nil)),
		vectordata.ContainsAny(vectordata.Column("Strings"), "x", nil), vectordata.ContainsAll(vectordata.Column("Ints"), 1, nil),
		vectordata.And(vectordata.Exists(n), vectordata.Or(vectordata.Eq(n, 1), vectordata.Not(vectordata.Eq(n, nil)))),
	}
	for _, filter := range filters {
		frozen, err := freezeFilter(filter, d)
		if err != nil {
			t.Fatalf("freeze %#v: %v", filter, err)
		}
		for _, record := range records {
			want, err := vectordata.MatchModelFilter(filter, record, d)
			if err != nil {
				t.Fatal(err)
			}
			got, err := vectordata.MatchModelFilter(frozen, record, d)
			if err != nil || got != want {
				t.Fatalf("%#v: frozen=%#v got=%v want=%v error=%v", filter, frozen, got, want, err)
			}
		}
	}
}
