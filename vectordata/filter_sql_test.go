package vectordata

import (
	"errors"
	"reflect"
	"testing"
)

type unsupportedFilter struct{}

func (unsupportedFilter) isFilter() {}

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

	expectedSQL := `(("id" = $1) AND ((CASE WHEN (jsonb_extract_path_text("metadata", 'rank')) ~ '^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$' THEN ((jsonb_extract_path_text("metadata", 'rank'))::double precision > $2) ELSE FALSE END) OR (("metadata" #> ARRAY['flags', 'pinned']) IS NOT NULL)))`
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

func TestCompileFilterSQL_MetadataGtNumericUsesSafeCast(t *testing.T) {
	// Arrange
	filter := Gt(Metadata("rank"), 10)

	// Act
	sql, args, next, err := CompileFilterSQL(filter, testFilterConfig(), 1)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}
	expectedSQL := `(CASE WHEN (jsonb_extract_path_text("metadata", 'rank')) ~ '^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$' THEN ((jsonb_extract_path_text("metadata", 'rank'))::double precision > $1) ELSE FALSE END)`
	if sql != expectedSQL {
		t.Fatalf("unexpected SQL\nwant: %s\n got: %s", expectedSQL, sql)
	}
	expectedArgs := []any{float64(10)}
	if !reflect.DeepEqual(args, expectedArgs) {
		t.Fatalf("unexpected args\nwant: %#v\n got: %#v", expectedArgs, args)
	}
	if next != 2 {
		t.Fatalf("unexpected next arg index: %d", next)
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

func TestCompileFilterSQL_NilFilterReturnsNoClause(t *testing.T) {
	// Act
	sql, args, next, err := CompileFilterSQL(nil, testFilterConfig(), 0)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL: %v", err)
	}
	if sql != "" {
		t.Fatalf("expected empty SQL, got %q", sql)
	}
	if args != nil {
		t.Fatalf("expected nil args, got %#v", args)
	}
	if next != 1 {
		t.Fatalf("expected next arg 1, got %d", next)
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

func TestCompileFilterSQL_ColumnInFilter(t *testing.T) {
	// Arrange
	filter := In(Column("id"), "a", "b")

	// Act
	sql, args, next, err := CompileFilterSQL(filter, testFilterConfig(), 2)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}
	if sql != `("id" IN ($2, $3))` {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if !reflect.DeepEqual(args, []any{"a", "b"}) {
		t.Fatalf("unexpected args: %#v", args)
	}
	if next != 4 {
		t.Fatalf("unexpected next arg index: %d", next)
	}
}

func TestCompileFilterSQL_LtAndNotFilter(t *testing.T) {
	// Arrange
	filter := Not(Lt(Metadata("status"), "m"))

	// Act
	sql, args, next, err := CompileFilterSQL(filter, testFilterConfig(), 1)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}
	if sql != `(NOT (jsonb_extract_path_text("metadata", 'status') < $1))` {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if !reflect.DeepEqual(args, []any{"m"}) {
		t.Fatalf("unexpected args: %#v", args)
	}
	if next != 2 {
		t.Fatalf("unexpected next arg index: %d", next)
	}
}

func TestCompileFilterSQL_MetadataLtNumericUsesSafeCast(t *testing.T) {
	// Arrange
	filter := Lt(Metadata("rank"), uint8(10))

	// Act
	sql, args, next, err := CompileFilterSQL(filter, testFilterConfig(), 1)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}
	expectedSQL := `(CASE WHEN (jsonb_extract_path_text("metadata", 'rank')) ~ '^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$' THEN ((jsonb_extract_path_text("metadata", 'rank'))::double precision < $1) ELSE FALSE END)`
	if sql != expectedSQL {
		t.Fatalf("unexpected SQL\nwant: %s\n got: %s", expectedSQL, sql)
	}
	if !reflect.DeepEqual(args, []any{float64(10)}) {
		t.Fatalf("unexpected args: %#v", args)
	}
	if next != 2 {
		t.Fatalf("unexpected next arg index: %d", next)
	}
}

func TestCompileFilterSQL_EscapesMetadataPathSegments(t *testing.T) {
	// Arrange
	filter := Exists(Metadata("owner's", "flag"))

	// Act
	sql, args, next, err := CompileFilterSQL(filter, testFilterConfig(), 1)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}
	if sql != `(("metadata" #> ARRAY['owner''s', 'flag']) IS NOT NULL)` {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if args != nil {
		t.Fatalf("unexpected args: %#v", args)
	}
	if next != 1 {
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

func TestCompileFilterSQL_InvalidFilterTrees(t *testing.T) {
	tests := []struct {
		name   string
		filter Filter
	}{
		{name: "empty in", filter: In(Column("id"))},
		{name: "empty and", filter: And()},
		{name: "empty or", filter: Or()},
		{name: "and nil child", filter: And(Eq(Column("id"), "a"), nil)},
		{name: "or nil child", filter: Or(nil)},
		{name: "not nil child", filter: Not(nil)},
		{name: "unsupported node", filter: unsupportedFilter{}},
		{name: "json marshal error", filter: Eq(Metadata("bad"), func() {})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := CompileFilterSQL(tt.filter, testFilterConfig(), 1)
			if !errors.Is(err, ErrInvalidFilter) {
				t.Fatalf("expected ErrInvalidFilter, got %v", err)
			}
		})
	}
}

func TestCompileFilterSQL_InvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		filter Filter
		cfg    FilterSQLConfig
	}{
		{
			name:   "missing column map",
			filter: Eq(Column("id"), "a"),
			cfg:    FilterSQLConfig{MetadataExpr: `"metadata"`},
		},
		{
			name:   "empty mapped column expression",
			filter: Eq(Column("id"), "a"),
			cfg: FilterSQLConfig{
				ColumnExpr:   map[string]string{"id": ""},
				MetadataExpr: `"metadata"`,
			},
		},
		{
			name:   "missing metadata expression",
			filter: Eq(Metadata("category"), "news"),
			cfg: FilterSQLConfig{
				ColumnExpr: map[string]string{"id": `"id"`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := CompileFilterSQL(tt.filter, tt.cfg, 1)
			if !errors.Is(err, ErrInvalidFilter) {
				t.Fatalf("expected ErrInvalidFilter, got %v", err)
			}
		})
	}
}

func TestCompileFilterSQL_TrimsColumnFieldName(t *testing.T) {
	// Arrange
	filter := Eq(Column("  id  "), "r1")

	// Act
	sql, args, _, err := CompileFilterSQL(filter, testFilterConfig(), 1)

	// Assert
	if err != nil {
		t.Fatalf("CompileFilterSQL error: %v", err)
	}
	if sql != `("id" = $1)` {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if !reflect.DeepEqual(args, []any{"r1"}) {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestCompileFilterSQL_MetadataPathRejectsWhitespaceSegment(t *testing.T) {
	// Arrange
	filter := Eq(Metadata("rank", "   "), 1)

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
