package vectordata

import (
	"errors"
	"reflect"
	"testing"
)

func testFilterConfig() FilterSQLConfig {
	return FilterSQLConfig{
		ColumnExpr: map[string]string{
			"id":      `"id"`,
			"content": `"content"`,
		},
		MetadataExpr: `"metadata"`,
	}
}

func TestCompileFilterSQL_Complex(t *testing.T) {
	// Arrange
	filter := And(
		Eq(Column("id"), "r1"),
		Or(
			Gt(Metadata("rank"), 10),
			Exists(Metadata("flags", "pinned")),
		),
	)

	// Act
	sql, args, next, err := CompileFilterSQL(filter, testFilterConfig(), 1)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}

	expectedSQL := `(("id" = $1) AND (((jsonb_extract_path_text("metadata", 'rank'))::double precision > $2) OR (("metadata" #> ARRAY['flags', 'pinned']) IS NOT NULL)))`
	if sql != expectedSQL {
		t.Fatalf("unexpected SQL\nwant: %s\n got: %s", expectedSQL, sql)
	}

	expectedArgs := []any{"r1", float64(10)}
	if !reflect.DeepEqual(args, expectedArgs) {
		t.Fatalf("unexpected args\nwant: %#v\n got: %#v", expectedArgs, args)
	}

	if next != 3 {
		t.Fatalf("unexpected next arg index: want 3 got %d", next)
	}
}

func TestCompileFilterSQL_StartArgOffset(t *testing.T) {
	// Arrange
	filter := Eq(Column("content"), "hello")

	// Act
	sql, args, next, err := CompileFilterSQL(filter, testFilterConfig(), 5)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}
	if sql != `("content" = $5)` {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if !reflect.DeepEqual(args, []any{"hello"}) {
		t.Fatalf("unexpected args: %#v", args)
	}
	if next != 6 {
		t.Fatalf("unexpected next arg index: %d", next)
	}
}

func TestCompileFilterSQL_InvalidColumn(t *testing.T) {
	// Arrange
	filter := Eq(Column("unknown"), "x")

	// Act
	_, _, _, err := CompileFilterSQL(filter, testFilterConfig(), 1)

	// Assert
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("expected ErrInvalidFilter, got %v", err)
	}
}

func TestCompileFilterSQL_InFilter(t *testing.T) {
	// Arrange
	filter := In(Metadata("category"), "a", "b")

	// Act
	sql, args, next, err := CompileFilterSQL(filter, testFilterConfig(), 1)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}
	if sql != `(("metadata" #> ARRAY['category']) IN ($1::jsonb, $2::jsonb))` {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if !reflect.DeepEqual(args, []any{[]byte(`"a"`), []byte(`"b"`)}) {
		t.Fatalf("unexpected args: %#v", args)
	}
	if next != 3 {
		t.Fatalf("unexpected next arg index: %d", next)
	}
}

func TestCompileFilterSQL_MetadataEqFilter(t *testing.T) {
	// Arrange
	filter := Eq(Metadata("category"), "news")

	// Act
	sql, args, next, err := CompileFilterSQL(filter, testFilterConfig(), 1)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}
	if sql != `(("metadata" #> ARRAY['category']) = $1::jsonb)` {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if !reflect.DeepEqual(args, []any{[]byte(`"news"`)}) {
		t.Fatalf("unexpected args: %#v", args)
	}
	if next != 2 {
		t.Fatalf("unexpected next arg index: %d", next)
	}
}
