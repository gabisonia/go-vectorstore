package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// One typed value per indexed property. No affinity on value: SQLite retains
// integer/real/text/NULL storage classes, exactly as the canonical SQL compiler.
// Triggers also maintain projections for old binaries and non-native handles.
const scalarProjection = `SELECT r.collection,r.key,p.key,
 CASE WHEN json_extract(d.value,'$.Null')=1 THEN NULL
 WHEN p.value='float64' THEN CAST(json_extract(d.value,'$.Value') AS REAL)
 ELSE json_extract(d.value,'$.Value') END
 FROM __vectorstore_records r
 JOIN __vectorstore_scalar_indexes i ON i.collection=r.collection
 JOIN __vectorstore_collections c ON c.name=i.collection AND c.definition=i.definition
 JOIN json_each(i.properties) p
 LEFT JOIN json_each(CAST(r.payload AS TEXT),'$.Data') d ON d.key=p.key COLLATE BINARY`

func initializeScalarIndexes(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS __vectorstore_scalar_indexes (
 collection TEXT PRIMARY KEY REFERENCES __vectorstore_collections(name) ON DELETE CASCADE,
 definition TEXT NOT NULL, properties TEXT NOT NULL, version INTEGER NOT NULL CHECK(version=1))`,
		`CREATE TABLE IF NOT EXISTS __vectorstore_scalar_values (
 collection TEXT NOT NULL, key TEXT NOT NULL, property TEXT NOT NULL, value,
 PRIMARY KEY(collection,key,property),
 FOREIGN KEY(collection,key) REFERENCES __vectorstore_records(collection,key) ON DELETE CASCADE,
 FOREIGN KEY(collection) REFERENCES __vectorstore_scalar_indexes(collection) ON DELETE CASCADE) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS __vectorstore_scalar_lookup ON __vectorstore_scalar_values(collection,property,value,key)`,
		`CREATE TRIGGER IF NOT EXISTS __vectorstore_scalar_insert AFTER INSERT ON __vectorstore_records BEGIN
 INSERT INTO __vectorstore_scalar_values ` + scalarProjection + ` WHERE r.collection=NEW.collection AND r.key=NEW.key; END`,
		`CREATE TRIGGER IF NOT EXISTS __vectorstore_scalar_update AFTER UPDATE OF payload ON __vectorstore_records BEGIN
 DELETE FROM __vectorstore_scalar_values WHERE collection=OLD.collection AND key=OLD.key;
 INSERT INTO __vectorstore_scalar_values ` + scalarProjection + ` WHERE r.collection=NEW.collection AND r.key=NEW.key; END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collection) scalarIndexProperties() map[string]vectordata.DataType {
	properties := map[string]vectordata.DataType{}
	for _, p := range c.definition.Data {
		if p.IsIndexed {
			switch p.Type {
			case vectordata.TypeString, vectordata.TypeInt64, vectordata.TypeFloat64, vectordata.TypeBool:
				properties[p.Name] = p.Type
			}
		}
	}
	return properties
}

// RebuildScalarIndexes atomically builds/backfills supported IsIndexed fields.
// Existing format-1 collections are never backfilled implicitly by reads, writes
// or EnsureExists. NativeIndexes must be enabled on this handle.
func (c *Collection) RebuildScalarIndexes(ctx context.Context) (err error) {
	defer func() { err = c.operation("rebuild_scalar_indexes", err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.store.nativeIndexes {
		return vectordata.ErrUnsupported
	}
	tx, err := c.store.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := c.metadata(ctx, tx); err != nil {
		return err
	}
	if err := c.rebuildScalarIndexes(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Collection) rebuildScalarIndexes(ctx context.Context, tx *sql.Tx) error {
	// Validate canonical records before publishing readiness, without embedding
	// callbacks or retaining a collection-sized decoded snapshot.
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM __vectorstore_records WHERE collection=?`, c.name)
	if err != nil {
		return err
	}
	defer rows.Close()
	d := c.readDefinition()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		record, err := decodeRecord(payload, d)
		if err != nil {
			return err
		}
		if _, err := vectordata.PrepareModelRecords(ctx, d, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{record}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	properties, err := json.Marshal(c.scalarIndexProperties())
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO __vectorstore_scalar_indexes(collection,definition,properties,version) VALUES(?,?,?,1)
 ON CONFLICT(collection) DO UPDATE SET definition=excluded.definition,properties=excluded.properties,version=1`, c.name, c.registration, string(properties)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM __vectorstore_scalar_values WHERE collection=?`, c.name); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO __vectorstore_scalar_values `+scalarProjection+` WHERE r.collection=?`, c.name)
	return err
}

// Select candidate keys using the value index, then keep the complete canonical
// predicate as a residual. Only positive predicates can restrict candidates:
// SQL UNKNOWN under NOT must not turn into a false membership test.
func (c *Collection) indexedScalarPlan(ctx context.Context, tx *sql.Tx, plan scalarQueryPlan) (scalarQueryPlan, error) {
	if !c.store.nativeIndexes || len(c.scalarIndexProperties()) == 0 {
		return plan, nil
	}
	var definition, properties string
	var version int
	err := tx.QueryRowContext(ctx, `SELECT definition,properties,version FROM __vectorstore_scalar_indexes WHERE collection=?`, c.name).Scan(&definition, &properties, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	expected, err := json.Marshal(c.scalarIndexProperties())
	if err != nil {
		return plan, err
	}
	if version != 1 || definition != c.registration || properties != string(expected) {
		return plan, fmt.Errorf("%w: scalar index registration differs; rebuild explicitly", vectordata.ErrSchemaMismatch)
	}
	compiler := scalarQueryCompiler{definition: c.definition, args: slices.Clone(plan.args), fields: map[string]string{}}
	for name := range c.scalarIndexProperties() {
		compiler.fields[name] = "s.value"
	}
	predicate, ok, err := compiler.scalarCandidates(plan.filter)
	if err != nil || !ok || len(compiler.args) > 900 {
		return plan, err
	}
	plan.sql = plan.sql[:plan.whereEnd] + " AND (" + predicate + ")" + plan.sql[plan.whereEnd:]
	plan.args = compiler.args
	return plan, nil
}

func (c *scalarQueryCompiler) scalarCandidates(filter vectordata.Filter) (string, bool, error) {
	var field vectordata.FieldRef
	var values []any
	var op string
	switch f := filter.(type) {
	case vectordata.EqFilter:
		field, values, op = f.Field, []any{f.Value}, "="
	case vectordata.InFilter:
		field, values, op = f.Field, f.Values, "in"
	case vectordata.GtFilter:
		field, values, op = f.Field, []any{f.Value}, ">"
	case vectordata.GteFilter:
		field, values, op = f.Field, []any{f.Value}, ">="
	case vectordata.LtFilter:
		field, values, op = f.Field, []any{f.Value}, "<"
	case vectordata.LteFilter:
		field, values, op = f.Field, []any{f.Value}, "<="
	case vectordata.ExistsFilter:
		field, op = f.Field, "exists"
	case vectordata.AndFilter:
		return c.scalarCandidateGroup(f.Children, false)
	case vectordata.OrFilter:
		return c.scalarCandidateGroup(f.Children, true)
	default:
		return "", false, nil
	}
	ref, err := vectordata.NormalizeFieldRef(field)
	if err != nil {
		return "", false, err
	}
	if ref.Kind != vectordata.FieldColumn || c.fields[ref.Name] != "s.value" {
		return "", false, nil
	}
	predicate, ok, err := c.compare(ref, values, op)
	if err != nil || !ok {
		return "", ok, err
	}
	return "r.key IN (SELECT s.key FROM __vectorstore_scalar_values s WHERE s.collection=?1 AND s.property=" + c.bind(ref.Name) + " AND " + predicate + ")", true, nil
}

func (c *scalarQueryCompiler) scalarCandidateGroup(children []vectordata.Filter, union bool) (string, bool, error) {
	var parts []string
	start := len(c.args)
	for _, child := range children {
		before := len(c.args)
		predicate, ok, err := c.scalarCandidates(child)
		if err != nil {
			return "", false, err
		}
		if !ok {
			if union {
				c.args = c.args[:start]
				return "", false, nil
			}
			c.args = c.args[:before]
			continue
		}
		parts = append(parts, predicate)
	}
	join := " AND "
	if union {
		join = " OR "
	}
	return "(" + strings.Join(parts, join) + ")", len(parts) != 0, nil
}
