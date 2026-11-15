// Package filtersql compiles portable filter ASTs into PostgreSQL predicates.
package filtersql

import (
	"encoding/json"
	"fmt"
	"github.com/gabisonia/go-vectorstore/vectordata/filterast"
	"strings"
)

const numericTextPattern = filterast.NumericTextPattern

// FilterSQLConfig configures filter compilation into SQL expressions.
type FilterSQLConfig struct {
	JSONColumnExpr map[string]string
	ArrayColumns   map[string]bool
	// ArrayElementTypes supplies trusted SQL scalar types for membership operands.
	// Leave a type unset to retain the original uncast string-array expression.
	ArrayElementTypes     map[string]string
	NormalizeArrayElement func(string, any) (any, error)
	// ColumnExpr maps logical column names to pre-quoted SQL expressions.
	ColumnExpr map[string]string
	// NormalizeColumnValue optionally canonicalizes native values before binding.
	// It is not called for JSON columns/paths, whose value semantics stay JSON.
	NormalizeColumnValue func(string, any) (any, error)
	// MetadataExpr is the SQL expression of the metadata JSONB column.
	MetadataExpr string
	// UseMetadataContainment adds GIN-compatible prefilters to metadata Eq/In.
	// Exact extraction comparisons remain in place to preserve equality semantics.
	UseMetadataContainment bool
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
	case GteFilter:
		return c.compileCompare(node.Field, node.Value, ">=")
	case LteFilter:
		return c.compileCompare(node.Field, node.Value, "<=")
	case filterast.ContainsAnyFilter:
		return c.compileContainsMany(node.Field, node.Values, false)
	case filterast.ContainsAllFilter:
		return c.compileContainsMany(node.Field, node.Values, true)
	case ContainsFilter:
		expr, metadata, path, err := c.resolveField(node.Field)
		if err != nil {
			return "", err
		}
		if !metadata {
			name := strings.TrimSpace(node.Field.Name)
			if !c.cfg.ArrayColumns[name] {
				return "", fmt.Errorf("%w: Contains requires an array property", ErrInvalidFilter)
			}
			value := node.Value
			if c.cfg.NormalizeArrayElement != nil {
				value, err = c.cfg.NormalizeArrayElement(name, value)
				if err != nil {
					return "", fmt.Errorf("%w: invalid membership value: %v", ErrInvalidFilter, err)
				}
			}
			ph := c.bind(value)
			if typ := c.cfg.ArrayElementTypes[name]; typ != "" {
				ph += "::" + typ
			}
			return fmt.Sprintf("(%s @> ARRAY[%s])", expr, ph), nil
		}
		ph, err := c.bindJSONB(node.Value)
		if err != nil {
			return "", err
		}
		value := metadataPathJSONBExpr(expr, path)
		exact := fmt.Sprintf("(CASE WHEN jsonb_typeof(%s) = 'array' THEN EXISTS (SELECT 1 FROM jsonb_array_elements(%s) AS __vs_item(value) WHERE __vs_item.value = %s::jsonb) ELSE FALSE END)", value, value, ph)
		return c.withMetadataContainment(exact, expr, path, []any{[]any{node.Value}})
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
		// Containment can turn SQL UNKNOWN for a missing path into FALSE.
		// Disable prefilters below NOT so missing/null behavior stays intact.
		useContainment := c.cfg.UseMetadataContainment
		c.cfg.UseMetadataContainment = false
		childSQL, err := c.compile(node.Child)
		c.cfg.UseMetadataContainment = useContainment
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
		if c.cfg.JSONColumnExpr[strings.TrimSpace(node.Field.Name)] != "" {
			ph, err := c.bindJSONB(node.Value)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("(%s = %s::jsonb)", fieldExpr, ph), nil
		}
		ph, err := c.bindColumn(node.Field, node.Value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(%s = %s)", fieldExpr, ph), nil
	}
	ph, err := c.bindJSONB(node.Value)
	if err != nil {
		return "", err
	}
	exact := fmt.Sprintf("(%s = %s::jsonb)", metadataPathJSONBExpr(fieldExpr, path), ph)
	return c.withMetadataContainment(exact, fieldExpr, path, []any{node.Value})
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
		if isMetadata || c.cfg.JSONColumnExpr[strings.TrimSpace(node.Field.Name)] != "" && !isNilValue(v) {
			ph, err := c.bindJSONB(v)
			if err != nil {
				return "", err
			}
			parts = append(parts, fmt.Sprintf("%s::jsonb", ph))
			continue
		}
		ph, err := c.bindColumn(node.Field, v)
		if err != nil {
			return "", err
		}
		parts = append(parts, ph)
	}
	if !isMetadata {
		return fmt.Sprintf("(%s IN (%s))", fieldExpr, strings.Join(parts, ", ")), nil
	}
	exact := fmt.Sprintf("(%s IN (%s))", metadataPathJSONBExpr(fieldExpr, path), strings.Join(parts, ", "))
	return c.withMetadataContainment(exact, fieldExpr, path, node.Values)
}

func (c *filterCompiler) withMetadataContainment(exact, metadataExpr string, path []string, values []any) (string, error) {
	if !c.cfg.UseMetadataContainment {
		return exact, nil
	}
	for _, segment := range path {
		// #> accepts array indexes as well as object keys. A JSON object
		// containment predicate cannot represent array traversal safely.
		digits := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(segment), "+"), "-")
		if digits != "" && strings.Trim(digits, "0123456789") == "" {
			return exact, nil
		}
	}
	predicates := make([]string, 0, len(values))
	for _, value := range values {
		for i := len(path) - 1; i >= 0; i-- {
			value = map[string]any{path[i]: value}
		}
		ph, err := c.bindJSONB(value)
		if err != nil {
			return "", err
		}
		predicates = append(predicates, fmt.Sprintf("%s @> %s::jsonb", metadataExpr, ph))
	}
	return fmt.Sprintf("((%s) AND %s)", strings.Join(predicates, " OR "), exact), nil
}

func (c *filterCompiler) compileGt(node GtFilter) (string, error) {
	return c.compileCompare(node.Field, node.Value, ">")
}
func (c *filterCompiler) compileLt(node LtFilter) (string, error) {
	return c.compileCompare(node.Field, node.Value, "<")
}
func (c *filterCompiler) compileCompare(field FieldRef, value any, op string) (string, error) {
	expr, metadata, path, err := c.resolveField(field)
	if err != nil {
		return "", err
	}
	if !metadata {
		ph, err := c.bindColumn(field, value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(%s %s %s)", expr, op, ph), nil
	}
	if number, numeric, err := numericLiteral(value); err != nil {
		return "", err
	} else if numeric {
		return c.compileMetadataNumericCompare(expr, path, op, number), nil
	}
	return fmt.Sprintf("(%s %s %s)", metadataPathTextExpr(expr, path), op, c.bind(fmt.Sprint(value))), nil
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
	case FieldDataPath:
		expr, ok := c.cfg.JSONColumnExpr[normalized.Name]
		if !ok {
			return "", false, nil, fmt.Errorf("%w: unknown JSON property %q", ErrInvalidFilter, normalized.Name)
		}
		return expr, true, normalized.Path, nil
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

func (c *filterCompiler) bindColumn(field FieldRef, value any) (string, error) {
	if !isNilValue(value) && c.cfg.NormalizeColumnValue != nil && c.cfg.JSONColumnExpr[strings.TrimSpace(field.Name)] == "" {
		var err error
		value, err = c.cfg.NormalizeColumnValue(strings.TrimSpace(field.Name), value)
		if err != nil {
			return "", fmt.Errorf("%w: invalid value for %q: %v", ErrInvalidFilter, field.Name, err)
		}
	} else if isNilValue(value) {
		value = nil
	}
	return c.bind(value), nil
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
	if len(path) == 0 {
		return metadataExpr
	}
	return fmt.Sprintf("(%s #> ARRAY[%s])", metadataExpr, pathArraySQL(path))
}

func metadataPathTextExpr(metadataExpr string, path []string) string {
	if len(path) == 0 {
		return fmt.Sprintf("(%s #>> '{}'::text[])", metadataExpr)
	}
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

type FieldRef = filterast.FieldRef
type FieldKind = filterast.FieldKind
type Filter = filterast.Filter
type EqFilter = filterast.EqFilter
type InFilter = filterast.InFilter
type GtFilter = filterast.GtFilter
type LtFilter = filterast.LtFilter
type GteFilter = filterast.GteFilter
type LteFilter = filterast.LteFilter
type ContainsFilter = filterast.ContainsFilter
type ExistsFilter = filterast.ExistsFilter
type AndFilter = filterast.AndFilter
type OrFilter = filterast.OrFilter
type NotFilter = filterast.NotFilter

const (
	FieldColumn   = filterast.FieldColumn
	FieldMetadata = filterast.FieldMetadata
	FieldDataPath = filterast.FieldDataPath
)

var (
	ErrInvalidFilter  = filterast.ErrInvalidFilter
	NormalizeFieldRef = filterast.NormalizeFieldRef
	numericLiteral    = filterast.NumericLiteral
	isNilValue        = filterast.IsNilValue
)

func (c *filterCompiler) compileContainsMany(field FieldRef, values []any, all bool) (string, error) {
	expr, metadata, path, err := c.resolveField(field)
	if err != nil {
		return "", err
	}
	if metadata {
		if len(values) == 0 {
			if !all {
				return "FALSE", nil
			}
			return "(COALESCE(jsonb_typeof(" + metadataPathJSONBExpr(expr, path) + ") = 'array', FALSE))", nil
		}
		children := make([]Filter, len(values))
		for i, v := range values {
			children[i] = filterast.Contains(field, v)
		}
		op := "OR"
		if all {
			op = "AND"
		}
		return c.compileLogical(op, children)
	}
	name := strings.TrimSpace(field.Name)
	if !c.cfg.ArrayColumns[name] {
		return "", fmt.Errorf("%w: array property required", ErrInvalidFilter)
	}
	typ := c.cfg.ArrayElementTypes[name]
	if typ == "" {
		typ = "text"
	}
	operands := make([]string, len(values))
	for i, v := range values {
		if c.cfg.NormalizeArrayElement != nil {
			v, err = c.cfg.NormalizeArrayElement(name, v)
			if err != nil {
				return "", fmt.Errorf("%w: invalid membership value: %v", ErrInvalidFilter, err)
			}
		}
		operands[i] = c.bind(v) + "::" + typ
	}
	op := "&&"
	if all {
		op = "@>"
	}
	return "(" + expr + " " + op + " ARRAY[" + strings.Join(operands, ",") + "]::" + typ + "[])", nil
}
