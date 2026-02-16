package mssql

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestCompileMSSQLFilterSQL_TrimsColumnReference(t *testing.T) {
	sql, args, next, err := compileMSSQLFilterSQL(vectordata.Eq(vectordata.Column("  id "), "doc-1"), 1)
	if err != nil {
		t.Fatalf("compileMSSQLFilterSQL: %v", err)
	}
	if sql != `([id] = @p1)` {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if !reflect.DeepEqual(args, []any{"doc-1"}) {
		t.Fatalf("unexpected args: %#v", args)
	}
	if next != 2 {
		t.Fatalf("unexpected next arg index: %d", next)
	}
}

func TestCompileMSSQLFilterSQL_MetadataExists(t *testing.T) {
	sql, args, _, err := compileMSSQLFilterSQL(vectordata.Exists(vectordata.Metadata(" nested ", "value")), 3)
	if err != nil {
		t.Fatalf("compileMSSQLFilterSQL: %v", err)
	}
	expectedSQL := `(JSON_PATH_EXISTS([metadata], @p3) = 1)`
	if sql != expectedSQL {
		t.Fatalf("unexpected SQL:\nwant: %s\n got: %s", expectedSQL, sql)
	}
	if !reflect.DeepEqual(args, []any{`$."nested"."value"`}) {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestCompileMSSQLFilterSQL_MetadataEqAndGt(t *testing.T) {
	filter := vectordata.And(
		vectordata.Eq(vectordata.Metadata("category"), "news"),
		vectordata.Gt(vectordata.Metadata("rank"), 1),
	)

	sql, args, next, err := compileMSSQLFilterSQL(filter, 2)
	if err != nil {
		t.Fatalf("compileMSSQLFilterSQL: %v", err)
	}

	expectedSQL := `((JSON_VALUE([metadata], @p2) = @p3) AND (TRY_CONVERT(float, JSON_VALUE([metadata], @p4)) > @p5))`
	if sql != expectedSQL {
		t.Fatalf("unexpected SQL:\nwant: %s\n got: %s", expectedSQL, sql)
	}
	expectedArgs := []any{`$."category"`, "news", `$."rank"`, float64(1)}
	if !reflect.DeepEqual(args, expectedArgs) {
		t.Fatalf("unexpected args:\nwant: %#v\n got: %#v", expectedArgs, args)
	}
	if next != 6 {
		t.Fatalf("unexpected next arg index: %d", next)
	}
}

func TestCompileMSSQLFilterSQL_UnsupportedColumnValueType(t *testing.T) {
	_, _, _, err := compileMSSQLFilterSQL(vectordata.Eq(vectordata.Column("id"), 123), 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errFilterPushdownUnsupported) {
		t.Fatalf("expected errFilterPushdownUnsupported, got %v", err)
	}
}
