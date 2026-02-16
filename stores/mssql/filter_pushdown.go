package mssql

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

var errFilterPushdownUnsupported = errors.New("mssql filter pushdown unsupported")

func compileMSSQLFilterSQL(filter vectordata.Filter, startArg int) (sql string, args []any, nextArg int, err error) {
	if startArg < 1 {
		startArg = 1
	}
	if filter == nil {
		return "", nil, startArg, nil
	}

	c := &mssqlFilterCompiler{
		nextArg: startArg,
	}
	out, err := c.compile(filter)
	if err != nil {
		return "", nil, startArg, err
	}
	return out, c.args, c.nextArg, nil
}

type mssqlFilterCompiler struct {
	args    []any
	nextArg int
}

func (c *mssqlFilterCompiler) compile(filter vectordata.Filter) (string, error) {
	switch node := filter.(type) {
	case vectordata.EqFilter:
		return c.compileEq(node)
	case vectordata.InFilter:
		return c.compileIn(node)
	case vectordata.GtFilter:
		return c.compileGt(node)
	case vectordata.LtFilter:
		return c.compileLt(node)
	case vectordata.ExistsFilter:
		return c.compileExists(node)
	case vectordata.AndFilter:
		return c.compileLogical("AND", node.Children)
	case vectordata.OrFilter:
		return c.compileLogical("OR", node.Children)
	case vectordata.NotFilter:
		if node.Child == nil {
			return "", fmt.Errorf("%w: NOT requires a child", vectordata.ErrInvalidFilter)
		}
		childSQL, err := c.compile(node.Child)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(NOT %s)", childSQL), nil
	default:
		return "", fmt.Errorf("%w: unsupported node type %T", vectordata.ErrInvalidFilter, filter)
	}
}

func (c *mssqlFilterCompiler) compileEq(node vectordata.EqFilter) (string, error) {
	columnExpr, metadataPath, isMetadata, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}
	if !isMetadata {
		value, ok := node.Value.(string)
		if !ok {
			return "", unsupportedPushdown("column equality only supports string values")
		}
		return fmt.Sprintf("(%s = %s)", columnExpr, c.bind(value)), nil
	}
	return c.compileMetadataEq(metadataPath, node.Value)
}

func (c *mssqlFilterCompiler) compileIn(node vectordata.InFilter) (string, error) {
	if len(node.Values) == 0 {
		return "", fmt.Errorf("%w: IN requires at least one value", vectordata.ErrInvalidFilter)
	}

	columnExpr, metadataPath, isMetadata, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}

	if !isMetadata {
		parts := make([]string, 0, len(node.Values))
		for _, value := range node.Values {
			text, ok := value.(string)
			if !ok {
				return "", unsupportedPushdown("column IN only supports string values")
			}
			parts = append(parts, c.bind(text))
		}
		return fmt.Sprintf("(%s IN (%s))", columnExpr, strings.Join(parts, ", ")), nil
	}

	predicates := make([]string, 0, len(node.Values))
	for _, value := range node.Values {
		predicate, err := c.compileMetadataEq(metadataPath, value)
		if err != nil {
			return "", err
		}
		predicates = append(predicates, predicate)
	}
	return fmt.Sprintf("(%s)", strings.Join(predicates, " OR ")), nil
}

func (c *mssqlFilterCompiler) compileGt(node vectordata.GtFilter) (string, error) {
	columnExpr, metadataPath, isMetadata, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}
	if !isMetadata {
		value, ok := node.Value.(string)
		if !ok {
			return "", unsupportedPushdown("column greater-than only supports string values")
		}
		return fmt.Sprintf("(%s > %s)", columnExpr, c.bind(value)), nil
	}

	if number, ok := toFloat64(node.Value); ok {
		pathPlaceholder := c.bind(metadataPath)
		valueExpr := fmt.Sprintf("JSON_VALUE(%s, %s)", quoteIdent(metadataColumn), pathPlaceholder)
		return fmt.Sprintf("(TRY_CONVERT(float, %s) > %s)", valueExpr, c.bind(number)), nil
	}
	return "", unsupportedPushdown("metadata greater-than only supports numeric values")
}

func (c *mssqlFilterCompiler) compileLt(node vectordata.LtFilter) (string, error) {
	columnExpr, metadataPath, isMetadata, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}
	if !isMetadata {
		value, ok := node.Value.(string)
		if !ok {
			return "", unsupportedPushdown("column less-than only supports string values")
		}
		return fmt.Sprintf("(%s < %s)", columnExpr, c.bind(value)), nil
	}

	if number, ok := toFloat64(node.Value); ok {
		pathPlaceholder := c.bind(metadataPath)
		valueExpr := fmt.Sprintf("JSON_VALUE(%s, %s)", quoteIdent(metadataColumn), pathPlaceholder)
		return fmt.Sprintf("(TRY_CONVERT(float, %s) < %s)", valueExpr, c.bind(number)), nil
	}
	return "", unsupportedPushdown("metadata less-than only supports numeric values")
}

func (c *mssqlFilterCompiler) compileExists(node vectordata.ExistsFilter) (string, error) {
	columnExpr, metadataPath, isMetadata, err := c.resolveField(node.Field)
	if err != nil {
		return "", err
	}
	if !isMetadata {
		return fmt.Sprintf("(%s IS NOT NULL)", columnExpr), nil
	}
	pathPlaceholder := c.bind(metadataPath)
	return fmt.Sprintf("(JSON_PATH_EXISTS(%s, %s) = 1)", quoteIdent(metadataColumn), pathPlaceholder), nil
}

func (c *mssqlFilterCompiler) compileLogical(op string, children []vectordata.Filter) (string, error) {
	if len(children) == 0 {
		return "", fmt.Errorf("%w: %s requires at least one child", vectordata.ErrInvalidFilter, op)
	}

	parts := make([]string, 0, len(children))
	for _, child := range children {
		if child == nil {
			return "", fmt.Errorf("%w: %s contains nil child", vectordata.ErrInvalidFilter, op)
		}
		childSQL, err := c.compile(child)
		if err != nil {
			return "", err
		}
		parts = append(parts, childSQL)
	}
	return fmt.Sprintf("(%s)", strings.Join(parts, fmt.Sprintf(" %s ", op))), nil
}

func (c *mssqlFilterCompiler) resolveField(ref vectordata.FieldRef) (columnExpr string, metadataPath string, isMetadata bool, err error) {
	normalized, err := vectordata.NormalizeFieldRef(ref)
	if err != nil {
		return "", "", false, err
	}

	switch normalized.Kind {
	case vectordata.FieldColumn:
		switch normalized.Name {
		case idColumn:
			return quoteIdent(idColumn), "", false, nil
		case contentColumn:
			return quoteIdent(contentColumn), "", false, nil
		default:
			return "", "", false, fmt.Errorf("%w: unknown column %q", vectordata.ErrInvalidFilter, normalized.Name)
		}
	case vectordata.FieldMetadata:
		return "", metadataPathLiteral(normalized.Path), true, nil
	default:
		return "", "", false, fmt.Errorf("%w: unsupported field kind %q", vectordata.ErrInvalidFilter, normalized.Kind)
	}
}

func (c *mssqlFilterCompiler) compileMetadataEq(metadataPath string, value any) (string, error) {
	pathPlaceholder := c.bind(metadataPath)
	valueExpr := fmt.Sprintf("JSON_VALUE(%s, %s)", quoteIdent(metadataColumn), pathPlaceholder)

	switch typed := value.(type) {
	case nil:
		return "", unsupportedPushdown("metadata equality with nil is not supported by SQL pushdown")
	case string:
		return fmt.Sprintf("(%s = %s)", valueExpr, c.bind(typed)), nil
	case bool:
		boolValue := "false"
		if typed {
			boolValue = "true"
		}
		return fmt.Sprintf("(LOWER(%s) = %s)", valueExpr, c.bind(boolValue)), nil
	default:
		if number, ok := toFloat64(value); ok {
			return fmt.Sprintf("(TRY_CONVERT(float, %s) = %s)", valueExpr, c.bind(number)), nil
		}
		return "", unsupportedPushdown("metadata scalar comparison does not support value type %T", value)
	}
}

func (c *mssqlFilterCompiler) bind(value any) string {
	placeholder := fmt.Sprintf("@p%d", c.nextArg)
	c.nextArg++
	c.args = append(c.args, value)
	return placeholder
}

func metadataPathLiteral(path []string) string {
	var b strings.Builder
	b.WriteString("$")
	for _, segment := range path {
		escaped := strings.ReplaceAll(segment, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		b.WriteString(`."`)
		b.WriteString(escaped)
		b.WriteString(`"`)
	}
	return b.String()
}

func unsupportedPushdown(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errFilterPushdownUnsupported, fmt.Sprintf(format, args...))
}
