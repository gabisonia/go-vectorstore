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
