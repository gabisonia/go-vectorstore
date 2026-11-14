package vectordata

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

const numericTextPattern = `^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$`

// FilterSQLConfig configures filter compilation into SQL expressions.
type FilterSQLConfig struct {
	// ColumnExpr maps logical column names to pre-quoted SQL expressions.
	ColumnExpr map[string]string
	// MetadataExpr is the SQL expression of the metadata JSONB column.
	MetadataExpr string
}

// CompileFilterSQL compiles a Filter tree into SQL WHERE fragment and args.
// Returned SQL does not include the WHERE keyword.
func CompileFilterSQL(filter Filter, cfg FilterSQLConfig, startArg int) (sql string, args []any, nextArg int, err error) {
	if startArg < 1 {
		startArg = 1
	}
	if filter == nil {
		return "", nil, startArg, nil
	}

	c := filterCompiler{
		cfg:     cfg,
		nextArg: startArg,
	}
	out, err := c.compile(filter)
	if err != nil {
		return "", nil, startArg, err
	}
	return out, c.args, c.nextArg, nil
}

type filterCompiler struct {
	cfg     FilterSQLConfig
	args    []any
	nextArg int
}

func (c *filterCompiler) compile(f Filter) (string, error) {
	switch node := f.(type) {
	case EqFilter:
		return c.compileEq(node)
	case InFilter:
		return c.compileIn(node)
	case GtFilter:
		return c.compileGt(node)
	case LtFilter:
		return c.compileLt(node)
	case ExistsFilter:
		return c.compileExists(node)
	case AndFilter:
		return c.compileLogical("AND", node.Children)
	case OrFilter:
		return c.compileLogical("OR", node.Children)
	case NotFilter:
		if node.Child == nil {
			return "", fmt.Errorf("%w: NOT requires a child", ErrInvalidFilter)
		}
		childSQL, err := c.compile(node.Child)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(NOT %s)", childSQL), nil
	default:
		return "", fmt.Errorf("%w: unsupported node type %T", ErrInvalidFilter, f)
	}
}

func (c *filterCompiler) compileEq(node EqFilter) (string, error) {
	fieldExpr, isMetadata, path, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}
	if !isMetadata {
		if isNilValue(node.Value) {
			return fmt.Sprintf("(%s IS NULL)", fieldExpr), nil
		}
		ph := c.bind(node.Value)
		return fmt.Sprintf("(%s = %s)", fieldExpr, ph), nil
	}
	ph, err := c.bindJSONB(node.Value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(%s = %s::jsonb)", metadataPathJSONBExpr(fieldExpr, path), ph), nil
}

func isNilValue(value any) bool {
	if value == nil {
		return true
	}
	switch v := reflect.ValueOf(value); v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (c *filterCompiler) compileIn(node InFilter) (string, error) {
	if len(node.Values) == 0 {
		return "", fmt.Errorf("%w: IN requires at least one value", ErrInvalidFilter)
	}
	fieldExpr, isMetadata, path, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(node.Values))
	for _, v := range node.Values {
		if isMetadata {
			ph, err := c.bindJSONB(v)
			if err != nil {
				return "", err
			}
			parts = append(parts, fmt.Sprintf("%s::jsonb", ph))
			continue
		}
		ph := c.bind(v)
		parts = append(parts, ph)
	}
	if !isMetadata {
		return fmt.Sprintf("(%s IN (%s))", fieldExpr, strings.Join(parts, ", ")), nil
	}
	return fmt.Sprintf("(%s IN (%s))", metadataPathJSONBExpr(fieldExpr, path), strings.Join(parts, ", ")), nil
}

func (c *filterCompiler) compileGt(node GtFilter) (string, error) {
	fieldExpr, isMetadata, path, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}
	if !isMetadata {
		return fmt.Sprintf("(%s > %s)", fieldExpr, c.bind(node.Value)), nil
	}
	if num, ok, err := numericLiteral(node.Value); err != nil {
		return "", err
	} else if ok {
		return c.compileMetadataNumericCompare(fieldExpr, path, ">", num), nil
	}
	return fmt.Sprintf("(%s > %s)", metadataPathTextExpr(fieldExpr, path), c.bind(fmt.Sprint(node.Value))), nil
}

func (c *filterCompiler) compileLt(node LtFilter) (string, error) {
	fieldExpr, isMetadata, path, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}
	if !isMetadata {
		return fmt.Sprintf("(%s < %s)", fieldExpr, c.bind(node.Value)), nil
	}
	if num, ok, err := numericLiteral(node.Value); err != nil {
		return "", err
	} else if ok {
		return c.compileMetadataNumericCompare(fieldExpr, path, "<", num), nil
	}
	return fmt.Sprintf("(%s < %s)", metadataPathTextExpr(fieldExpr, path), c.bind(fmt.Sprint(node.Value))), nil
}

func (c *filterCompiler) compileMetadataNumericCompare(metadataExpr string, path []string, op string, value string) string {
	textExpr := metadataPathTextExpr(metadataExpr, path)
	// A numeric-looking string can exceed even PostgreSQL numeric's range.
	// Keep the cast inside CASE so malformed metadata never aborts the query.
	return fmt.Sprintf(
		"(CASE WHEN (%s) ~ %s AND pg_input_is_valid(%s, 'numeric') THEN ((%s)::numeric %s %s::text::numeric) ELSE FALSE END)",
		textExpr,
		singleQuoted(numericTextPattern),
		textExpr,
		textExpr,
		op,
		c.bind(value),
	)
}

func (c *filterCompiler) compileExists(node ExistsFilter) (string, error) {
	fieldExpr, isMetadata, path, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}
	if !isMetadata {
		return fmt.Sprintf("(%s IS NOT NULL)", fieldExpr), nil
	}
	return fmt.Sprintf("(%s IS NOT NULL)", metadataPathJSONBExpr(fieldExpr, path)), nil
}

func (c *filterCompiler) compileLogical(op string, children []Filter) (string, error) {
	if len(children) == 0 {
		return "", fmt.Errorf("%w: %s requires at least one child", ErrInvalidFilter, op)
	}
	parts := make([]string, 0, len(children))
	for _, child := range children {
		if child == nil {
			return "", fmt.Errorf("%w: %s contains nil child", ErrInvalidFilter, op)
		}
		childSQL, err := c.compile(child)
		if err != nil {
			return "", err
		}
		parts = append(parts, childSQL)
	}
	return fmt.Sprintf("(%s)", strings.Join(parts, fmt.Sprintf(" %s ", op))), nil
}

func (c *filterCompiler) resolveField(ref FieldRef) (expr string, isMetadata bool, path []string, err error) {
	normalized, err := NormalizeFieldRef(ref)
	if err != nil {
		return "", false, nil, err
	}

	switch normalized.Kind {
	case FieldColumn:
		if c.cfg.ColumnExpr == nil {
			return "", false, nil, fmt.Errorf("%w: no column mapping configured", ErrInvalidFilter)
		}
		expr, ok := c.cfg.ColumnExpr[normalized.Name]
		if !ok || expr == "" {
			return "", false, nil, fmt.Errorf("%w: unknown column %q", ErrInvalidFilter, normalized.Name)
		}
		return expr, false, nil, nil
	case FieldMetadata:
		if c.cfg.MetadataExpr == "" {
			return "", false, nil, fmt.Errorf("%w: metadata expression not configured", ErrInvalidFilter)
		}
		cp := make([]string, len(normalized.Path))
		copy(cp, normalized.Path)
		return c.cfg.MetadataExpr, true, cp, nil
	default:
		return "", false, nil, fmt.Errorf("%w: unsupported field kind %q", ErrInvalidFilter, normalized.Kind)
	}
}

func (c *filterCompiler) bind(v any) string {
	ph := fmt.Sprintf("$%d", c.nextArg)
	c.nextArg++
	c.args = append(c.args, v)
	return ph
}

func (c *filterCompiler) bindJSONB(v any) (string, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("%w: JSON encode value: %v", ErrInvalidFilter, err)
	}
	ph := fmt.Sprintf("$%d", c.nextArg)
	c.nextArg++
	c.args = append(c.args, encoded)
	return ph, nil
}

func metadataPathJSONBExpr(metadataExpr string, path []string) string {
	return fmt.Sprintf("(%s #> ARRAY[%s])", metadataExpr, pathArraySQL(path))
}

func metadataPathTextExpr(metadataExpr string, path []string) string {
	parts := make([]string, 0, len(path))
	for _, p := range path {
		parts = append(parts, singleQuoted(p))
	}
	return fmt.Sprintf("jsonb_extract_path_text(%s, %s)", metadataExpr, strings.Join(parts, ", "))
}

func pathArraySQL(path []string) string {
	parts := make([]string, 0, len(path))
	for _, p := range path {
		parts = append(parts, singleQuoted(p))
	}
	return strings.Join(parts, ", ")
}

func singleQuoted(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

func numericLiteral(v any) (string, bool, error) {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		// JSON encoding preserves integer and decimal precision and rejects
		// non-finite floats and malformed json.Number values.
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", true, fmt.Errorf("%w: invalid numeric value: %v", ErrInvalidFilter, err)
		}
		return string(encoded), true, nil
	default:
		return "", false, nil
	}
}
