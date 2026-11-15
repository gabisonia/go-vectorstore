package filtersql

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata/filterast"
)

func TestNativeNormalizerPreservesJSONSemantics(t *testing.T) {
	calls := 0
	cfg := FilterSQLConfig{
		ColumnExpr:           map[string]string{"Owner": "t.owner", "Details": "t.details"},
		JSONColumnExpr:       map[string]string{"Details": "t.details"},
		NormalizeColumnValue: func(name string, value any) (any, error) { calls++; return "canonical", nil },
	}
	filter := filterast.AndFilter{Children: []Filter{
		filterast.EqFilter{Field: FieldRef{Kind: FieldColumn, Name: "Owner"}, Value: "uppercase"},
		filterast.InFilter{Field: FieldRef{Kind: FieldColumn, Name: "Owner"}, Values: []any{"uppercase", nil}},
		filterast.EqFilter{Field: FieldRef{Kind: FieldColumn, Name: "Details"}, Value: "uppercase"},
		filterast.EqFilter{Field: FieldRef{Kind: FieldDataPath, Name: "Details", Path: []string{"x"}}, Value: "uppercase"},
	}}
	_, args, _, err := CompileFilterSQL(filter, cfg, 1)
	if err != nil || calls != 2 || !reflect.DeepEqual(args, []any{"canonical", "canonical", nil, []byte(`"uppercase"`), []byte(`"uppercase"`)}) {
		t.Fatalf("normalization calls=%d, args=%v, error=%v", calls, args, err)
	}
	cfg.NormalizeColumnValue = func(string, any) (any, error) { return nil, errors.New("invalid UUID") }
	if _, _, _, err := CompileFilterSQL(filter, cfg, 1); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("normalizer error: %v", err)
	}
}

func TestNativeArrayMembershipCasts(t *testing.T) {
	cfg := FilterSQLConfig{ColumnExpr: map[string]string{"Ints": "t.i", "Floats": "t.f", "Flags": "t.b"}, ArrayColumns: map[string]bool{"Ints": true, "Floats": true, "Flags": true}, ArrayElementTypes: map[string]string{"Ints": "bigint", "Floats": "double precision", "Flags": "boolean"}}
	for _, tt := range []struct {
		field, cast string
		value       any
	}{{"Ints", "bigint", int64(2)}, {"Floats", "double precision", 0.25}, {"Flags", "boolean", false}} {
		sql, args, _, err := CompileFilterSQL(ContainsFilter{Field: FieldRef{Kind: FieldColumn, Name: tt.field}, Value: tt.value}, cfg, 2)
		want := "(" + cfg.ColumnExpr[tt.field] + " @> ARRAY[$2::" + tt.cast + "])"
		if err != nil || sql != want || !reflect.DeepEqual(args, []any{tt.value}) {
			t.Fatalf("membership %s: SQL=%s args=%v error=%v", tt.field, sql, args, err)
		}
	}
	cfg.NormalizeArrayElement = func(string, any) (any, error) { return nil, errors.New("invalid member") }
	if _, _, _, err := CompileFilterSQL(ContainsFilter{Field: FieldRef{Kind: FieldColumn, Name: "Ints"}, Value: "bad"}, cfg, 1); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("membership normalization error: %v", err)
	}
}

func TestArraySetNativePredicates(t *testing.T) {
	cfg := FilterSQLConfig{ColumnExpr: map[string]string{"Ints": "t.i"}, ArrayColumns: map[string]bool{"Ints": true}, ArrayElementTypes: map[string]string{"Ints": "bigint"}}
	for _, tt := range []struct {
		filter Filter
		want   string
		values []any
	}{
		{filterast.ContainsAny(filterast.Column("Ints"), 1, 2), "(t.i && ARRAY[$3::bigint,$4::bigint]::bigint[])", []any{1, 2}},
		{filterast.ContainsAll(filterast.Column("Ints"), 1, 2), "(t.i @> ARRAY[$3::bigint,$4::bigint]::bigint[])", []any{1, 2}},
		{filterast.ContainsAll(filterast.Column("Ints")), "(t.i @> ARRAY[]::bigint[])", nil},
	} {
		sql, args, next, err := CompileFilterSQL(tt.filter, cfg, 3)
		if err != nil || sql != tt.want || !reflect.DeepEqual(args, tt.values) || next != 3+len(tt.values) {
			t.Fatalf("SQL=%s args=%v next=%d err=%v", sql, args, next, err)
		}
	}
}
