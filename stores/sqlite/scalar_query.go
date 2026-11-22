package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type scalarQueryPlan struct {
	sql      string
	args     []any
	filter   vectordata.Filter
	whereEnd int
}

type scalarQueryCompiler struct {
	definition vectordata.CollectionDefinition
	args       []any
	nodes      int
	fields     map[string]string
}

func (c *scalarQueryCompiler) bind(v any) string {
	c.args = append(c.args, v)
	return fmt.Sprintf("?%d", len(c.args))
}

// Return no plan if any predicate/order cannot retain model semantics. Bounds
// also avoid replacing valid Go filters with SQLite expression/parameter limits.
func (c *Collection) scalarPlan(filter vectordata.Filter, top int, opts vectordata.QueryOptions, count bool) (scalarQueryPlan, bool, error) {
	compiler := scalarQueryCompiler{definition: c.definition, args: []any{c.name}, fields: map[string]string{}}
	where, ok, err := compiler.filter(filter, 0)
	if err != nil || !ok {
		return scalarQueryPlan{}, false, err
	}
	query := "SELECT count(*) FROM __vectorstore_records r WHERE r.collection=?1 AND (" + where + ")"
	whereEnd := len(query)
	if !count {
		if len(opts.OrderBy) > 64 {
			return scalarQueryPlan{}, false, nil
		}
		var order []string
		for _, field := range opts.OrderBy {
			parts, supported := compiler.order(field.Property, field.Descending)
			if !supported {
				return scalarQueryPlan{}, false, nil
			}
			order = append(order, parts...)
		}
		keyOrder, _ := compiler.order(c.definition.Key.Name, false)
		order = append(order, keyOrder...)
		query = "SELECT r.payload,r.key FROM __vectorstore_records r WHERE r.collection=?1 AND (" + where + ")"
		whereEnd = len(query)
		query += " ORDER BY " + strings.Join(order, ",") + " LIMIT " + compiler.bind(top) + " OFFSET " + compiler.bind(opts.Skip)
	}
	if len(compiler.args) > 900 {
		return scalarQueryPlan{}, false, nil
	}
	return scalarQueryPlan{sql: query, args: compiler.args, filter: filter, whereEnd: whereEnd}, true, nil
}

func (c *scalarQueryCompiler) field(name string) (string, vectordata.DataProperty, bool) {
	if name == c.definition.Key.Name {
		return "json_extract(CAST(r.payload AS TEXT),'$.Key')", vectordata.DataProperty{}, true
	}
	for _, p := range c.definition.Data {
		if p.Name != name {
			continue
		}
		switch p.Type {
		case vectordata.TypeString, vectordata.TypeInt64, vectordata.TypeFloat64, vectordata.TypeBool:
		default:
			return "", p, false
		}
		if expr, ok := c.fields[name]; ok {
			return expr, p, true
		}
		// json_each binds arbitrary logical names literally, including quotes,
		// dots and brackets, without treating them as JSON-path or SQL syntax.
		nameParam := c.bind(name)
		expr := "(SELECT CASE WHEN json_extract(__vs_property.value,'$.Null')=1 THEN NULL ELSE json_extract(__vs_property.value,'$.Value') END FROM json_each(CAST(r.payload AS TEXT),'$.Data') AS __vs_property WHERE __vs_property.key=" + nameParam + " COLLATE BINARY)"
		if p.Type == vectordata.TypeFloat64 {
			// A float64's shortest JSON spelling can be an integer token. Decode
			// it as REAL, as Go's typed decoder does, before comparing or sorting.
			expr = "CAST(" + expr + " AS REAL)"
		}
		c.fields[name] = expr
		return expr, p, true
	}
	return "", vectordata.DataProperty{}, false
}

func (c *scalarQueryCompiler) order(name string, desc bool) ([]string, bool) {
	direction := " ASC NULLS LAST"
	if desc {
		direction = " DESC NULLS LAST"
	}
	if name == c.definition.Key.Name && c.definition.Key.Type == vectordata.KeyUint64 {
		// Canonical unsigned JSON tokens have no sign, exponent or leading zeros.
		// Length then byte order preserves the full uint64 range without REAL casts.
		return []string{"length(r.key)" + direction, "r.key COLLATE BINARY" + direction}, true
	}
	expr, _, ok := c.field(name)
	return []string{expr + " COLLATE BINARY" + direction}, ok
}

func (c *scalarQueryCompiler) normalize(name string, p vectordata.DataProperty, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	var value any
	var err error
	if name == c.definition.Key.Name {
		value, err = c.definition.NormalizeKey(v)
	} else {
		value, err = vectordata.NormalizeModelData(v, p)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: invalid operand for %q: %v", vectordata.ErrInvalidFilter, name, err)
	}
	return value, nil
}

func (c *scalarQueryCompiler) compare(ref vectordata.FieldRef, values []any, op string) (string, bool, error) {
	ref, err := vectordata.NormalizeFieldRef(ref)
	if err != nil {
		return "", false, err
	}
	if ref.Kind != vectordata.FieldColumn {
		return "", false, nil
	}
	expr, p, ok := c.field(ref.Name)
	if !ok {
		return "", false, nil
	}
	key := ref.Name == c.definition.Key.Name
	if op == "exists" {
		if key {
			return "1", true, nil
		}
		return "(" + expr + " IS NOT NULL)", true, nil
	}
	var normalized []any
	for _, v := range values {
		value, err := c.normalize(ref.Name, p, v)
		if err != nil {
			return "", false, err
		}
		if !validText(value) {
			// Invalid UTF-8 cannot match stored keys; preserve UNKNOWN for IN's
			// NULL operand. Other string operations keep the exact Go fallback.
			if key && (op == "=" || op == "in") {
				continue
			}
			return "", false, nil
		}
		if key && value != nil && (op == "=" || op == "in") {
			value = keyToken(value)
		}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return "0", true, nil
	}
	if key && (op == "=" || op == "in") {
		expr = "r.key"
	}
	if op == "in" {
		payload, err := json.Marshal(normalized)
		if err != nil {
			return "", false, err
		}
		valueExpr := "value"
		if !key && p.Type == vectordata.TypeFloat64 {
			valueExpr = "CAST(value AS REAL)"
		}
		return "(" + expr + " COLLATE BINARY IN (SELECT " + valueExpr + " FROM json_each(" + c.bind(string(payload)) + ")))", true, nil
	}
	value := normalized[0]
	if value == nil {
		return "(" + expr + " IS NULL)", true, nil
	}
	ph := c.bind(value)
	if key && c.definition.Key.Type == vectordata.KeyUint64 && op != "=" {
		// Order comparisons use canonical tokens too, avoiding JSON extraction's
		// REAL representation for unsigned values above MaxInt64.
		c.args[len(c.args)-1] = keyToken(value)
		return "(length(r.key) " + op[:1] + " length(" + ph + ") OR (length(r.key)=length(" + ph + ") AND r.key COLLATE BINARY " + op + " " + ph + "))", true, nil
	}
	return "(" + expr + " COLLATE BINARY " + op + " " + ph + ")", true, nil
}

func (c *scalarQueryCompiler) filter(filter vectordata.Filter, depth int) (string, bool, error) {
	c.nodes++
	if depth > 16 || c.nodes > 128 {
		return "", false, nil
	}
	switch f := filter.(type) {
	case nil:
		return "1", true, nil
	case vectordata.EqFilter:
		return c.compare(f.Field, []any{f.Value}, "=")
	case vectordata.InFilter:
		return c.compare(f.Field, f.Values, "in")
	case vectordata.GtFilter:
		return c.compare(f.Field, []any{f.Value}, ">")
	case vectordata.GteFilter:
		return c.compare(f.Field, []any{f.Value}, ">=")
	case vectordata.LtFilter:
		return c.compare(f.Field, []any{f.Value}, "<")
	case vectordata.LteFilter:
		return c.compare(f.Field, []any{f.Value}, "<=")
	case vectordata.ExistsFilter:
		return c.compare(f.Field, nil, "exists")
	case vectordata.NotFilter:
		sql, ok, err := c.filter(f.Child, depth+1)
		return "(NOT " + sql + ")", ok, err
	case vectordata.AndFilter:
		return c.logical(f.Children, " AND ", depth)
	case vectordata.OrFilter:
		return c.logical(f.Children, " OR ", depth)
	default:
		return "", false, nil
	}
}

func (c *scalarQueryCompiler) logical(children []vectordata.Filter, join string, depth int) (string, bool, error) {
	var parts []string
	for _, child := range children {
		sql, ok, err := c.filter(child, depth+1)
		if !ok || err != nil {
			return "", ok, err
		}
		parts = append(parts, sql)
	}
	return "(" + strings.Join(parts, join) + ")", true, nil
}

func (c *Collection) scalarRead(ctx context.Context, plan scalarQueryPlan) ([]vectordata.ModelRecord, error) {
	tx, err := c.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := c.metadata(ctx, tx); err != nil {
		return nil, err
	}
	plan, err = c.indexedScalarPlan(ctx, tx, plan)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, plan.sql, plan.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []vectordata.ModelRecord
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var payload []byte
		var key string
		if err := rows.Scan(&payload, &key); err != nil {
			return nil, err
		}
		r, err := decodeStoredRecord(payload, key, c.definition)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Rollback(); err != nil {
		return nil, err
	}
	return vectordata.PrepareModelRecords(ctx, c.readDefinition(), vectordata.ModelCollectionOptions{}, records)
}

func (c *Collection) scalarCount(ctx context.Context, plan scalarQueryPlan) (int64, error) {
	tx, err := c.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := c.metadata(ctx, tx); err != nil {
		return 0, err
	}
	plan, err = c.indexedScalarPlan(ctx, tx, plan)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := tx.QueryRowContext(ctx, plan.sql, plan.args...).Scan(&count); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := tx.Rollback(); err != nil {
		return 0, err
	}
	return count, nil
}
