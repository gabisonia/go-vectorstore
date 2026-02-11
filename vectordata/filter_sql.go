package vectordata

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
		ph := c.bind(node.Value)
		return fmt.Sprintf("(%s = %s)", fieldExpr, ph), nil
	}
	ph, err := c.bindJSONB(node.Value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(%s = %s::jsonb)", metadataPathJSONBExpr(fieldExpr, path), ph), nil
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
	if num, ok := toFloat64(node.Value); ok {
		return fmt.Sprintf("((%s)::double precision > %s)", metadataPathTextExpr(fieldExpr, path), c.bind(num)), nil
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
	if num, ok := toFloat64(node.Value); ok {
		return fmt.Sprintf("((%s)::double precision < %s)", metadataPathTextExpr(fieldExpr, path), c.bind(num)), nil
	}
	return fmt.Sprintf("(%s < %s)", metadataPathTextExpr(fieldExpr, path), c.bind(fmt.Sprint(node.Value))), nil
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
	switch ref.Kind {
	case FieldColumn:
		if ref.Name == "" {
			return "", false, nil, fmt.Errorf("%w: column field name is empty", ErrInvalidFilter)
		}
		if c.cfg.ColumnExpr == nil {
			return "", false, nil, fmt.Errorf("%w: no column mapping configured", ErrInvalidFilter)
		}
		expr, ok := c.cfg.ColumnExpr[ref.Name]
		if !ok || expr == "" {
			return "", false, nil, fmt.Errorf("%w: unknown column %q", ErrInvalidFilter, ref.Name)
		}
		return expr, false, nil, nil
	case FieldMetadata:
		if c.cfg.MetadataExpr == "" {
			return "", false, nil, fmt.Errorf("%w: metadata expression not configured", ErrInvalidFilter)
		}
		if len(ref.Path) == 0 {
			return "", false, nil, fmt.Errorf("%w: metadata path is empty", ErrInvalidFilter)
		}
		cp := make([]string, len(ref.Path))
		copy(cp, ref.Path)
		return c.cfg.MetadataExpr, true, cp, nil
	default:
		return "", false, nil, fmt.Errorf("%w: unsupported field kind %q", ErrInvalidFilter, ref.Kind)
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

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}
