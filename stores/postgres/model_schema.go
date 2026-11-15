package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

const modelRegistry = "__vectorstore_models"

var _ vectordata.ModelStore = (*PostgresVectorStore)(nil)

func validCollectionName(name string) error {
	if name == "" || len(name) > 63 || strings.ContainsRune(name, 0) || name == modelRegistry {
		return fmt.Errorf("%w: invalid collection name", vectordata.ErrSchemaMismatch)
	}
	return nil
}
func (s *PostgresVectorStore) registryExists(ctx context.Context) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualifiedTable(s.opts.Schema, modelRegistry)).Scan(&exists)
	return exists, err
}
func (s *PostgresVectorStore) ListCollectionNames(ctx context.Context) ([]string, error) {
	query := `SELECT DISTINCT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p') AND EXISTS (SELECT 1 FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid WHERE a.attrelid=c.oid AND NOT a.attisdropped AND t.typname IN ('vector','halfvec'))`
	exists, err := s.registryExists(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		query += ` UNION SELECT m.name FROM ` + qualifiedTable(s.opts.Schema, modelRegistry) + ` m JOIN pg_class c ON c.relname=m.name JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p')`
	}
	query += " ORDER BY 1"
	rows, err := s.pool.Query(ctx, query, s.opts.Schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
func (s *PostgresVectorStore) CollectionExists(ctx context.Context, name string) (bool, error) {
	if err := validCollectionName(name); err != nil {
		return false, err
	}
	// Resolve only the requested relation. Legacy/vector model collections need
	// one catalog query; registered models without vectors need a registry lookup.
	var kind string
	var vector, registry bool
	err := s.pool.QueryRow(ctx, `SELECT c.relkind::text,
 EXISTS (SELECT 1 FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid
         WHERE a.attrelid=c.oid AND NOT a.attisdropped AND t.typname IN ('vector','halfvec')),
 to_regclass($2) IS NOT NULL
 FROM pg_class c WHERE c.oid=to_regclass($1)`, qualifiedTable(s.opts.Schema, name), qualifiedTable(s.opts.Schema, modelRegistry)).Scan(&kind, &vector, &registry)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if kind != "r" && kind != "p" {
		return false, nil
	}
	if vector {
		return true, nil
	}
	if !registry {
		return false, nil
	}
	var exists bool
	err = s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+qualifiedTable(s.opts.Schema, modelRegistry)+" WHERE name=$1)", name).Scan(&exists)
	return exists, err
}
func (s *PostgresVectorStore) DeleteCollection(ctx context.Context, name string) error {
	if err := validCollectionName(name); err != nil {
		return err
	}
	exists, err := s.CollectionExists(ctx, name)
	if err != nil || !exists {
		return err
	}
	registry, err := s.registryExists(ctx)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", qualifiedTable(s.opts.Schema, name)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DROP TABLE IF EXISTS "+qualifiedTable(s.opts.Schema, name)); err != nil {
		return err
	}
	if registry {
		if _, err := tx.Exec(ctx, "DELETE FROM "+qualifiedTable(s.opts.Schema, modelRegistry)+" WHERE name=$1", name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *PostgresVectorStore) GetModelCollection(name string, definition vectordata.CollectionDefinition, opts vectordata.ModelCollectionOptions) (vectordata.ModelCollection, error) {
	opts, err := opts.WithDefaults(vectordata.ModelCollectionOptions{Embedder: s.opts.Embedder, EmbeddingBatchSize: s.opts.EmbeddingBatchSize})
	if err != nil {
		return nil, err
	}
	if err := validCollectionName(name); err != nil {
		return nil, err
	}
	d, err := definition.Normalize()
	if err != nil {
		return nil, err
	}
	if err := validateVectorDimensions(d); err != nil {
		return nil, err
	}
	for _, p := range d.Vectors {
		if p.Index != nil {
			if _, err := buildVectorIndexWithClause(defaultIndexMethod(p.Index.Method), p.Index); err != nil {
				return nil, err
			}
		}
	}
	return &ModelCollection{store: s, name: name, definition: d, options: opts}, nil
}
func (s *PostgresVectorStore) EnsureModelCollection(ctx context.Context, name string, d vectordata.CollectionDefinition, opts vectordata.ModelCollectionOptions) (vectordata.ModelCollection, error) {
	c, err := s.GetModelCollection(name, d, opts)
	if err != nil {
		return nil, err
	}
	if err := c.EnsureExists(ctx); err != nil {
		return nil, err
	}
	return c, nil
}
func defaultIndexMethod(method vectordata.IndexMethod) vectordata.IndexMethod {
	if method == "" {
		return vectordata.IndexMethodHNSW
	}
	return method
}
func dataSQLType(t vectordata.DataType) string {
	switch t {
	case vectordata.TypeString:
		return "text"
	case vectordata.TypeInt64:
		return "bigint"
	case vectordata.TypeFloat64:
		return "double precision"
	case vectordata.TypeBool:
		return "boolean"
	case vectordata.TypeJSON:
		return "jsonb"
	case vectordata.TypeTime:
		return "timestamp with time zone"
	case vectordata.TypeStringArray:
		return "text[]"
	case vectordata.TypeUUID:
		return "uuid"
	case vectordata.TypeBytes:
		return "bytea"
	}
	panic("validated data type")
}
func keySQLType(t vectordata.KeyType) string {
	switch t {
	case vectordata.KeyString:
		return "text"
	case vectordata.KeyInt64:
		return "bigint"
	case vectordata.KeyUUID:
		return "uuid"
	}
	panic("validated key type")
}
func (c *ModelCollection) EnsureExists(ctx context.Context) (err error) {
	defer func() { err = c.operation("ensure", err) }()
	if err := c.store.ensureBaseSchema(ctx); err != nil {
		return err
	}
	if err := c.ensureRegistry(ctx); err != nil {
		return err
	}

	tx, err := c.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	if err := c.validateVectorTypes(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.table()); err != nil {
		return err
	}
	d := c.definition
	columns := []string{quoteIdent(d.Key.StorageName) + " " + keySQLType(d.Key.Type)}
	if d.Key.AutoGenerated {
		columns[0] += " GENERATED BY DEFAULT AS IDENTITY"
	}
	columns[0] += " PRIMARY KEY"
	for _, p := range d.Data {
		sql := quoteIdent(p.StorageName) + " " + dataSQLType(p.Type)
		if !p.Optional {
			sql += " NOT NULL"
		}
		columns = append(columns, sql)
	}
	for _, p := range d.Vectors {
		columns = append(columns, quoteIdent(p.StorageName)+" "+vectorColumnType(p)+" NOT NULL")
	}
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+c.table()+" ("+strings.Join(columns, ", ")+")"); err != nil {
		return err
	}
	if err := c.validateSchema(ctx, tx); err != nil {
		return err
	}
	payload, err := definitionJSON(d)
	if err != nil {
		return err
	}
	var previous []byte
	err = tx.QueryRow(ctx, "SELECT definition FROM "+qualifiedTable(c.store.opts.Schema, modelRegistry)+" WHERE name=$1", c.name).Scan(&previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		var same bool
		if err := tx.QueryRow(ctx, "SELECT $1::jsonb = $2::jsonb", previous, payload).Scan(&same); err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("%w: registered model definition differs", vectordata.ErrSchemaMismatch)
		}
	}
	indexes, err := modelIndexSpecs(c.name, d)
	if err != nil {
		return err
	}
	for _, index := range indexes {
		if err := c.ensureModelIndex(ctx, tx, index.name, index.method, index.column, index.opClass, index.with, index.fullText); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "INSERT INTO "+qualifiedTable(c.store.opts.Schema, modelRegistry)+" (name,definition) VALUES ($1,$2::jsonb) ON CONFLICT(name) DO NOTHING", c.name, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (c *ModelCollection) ensureRegistry(ctx context.Context) error {
	tx, err := c.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.store.opts.Schema+"/"+modelRegistry); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+qualifiedTable(c.store.opts.Schema, modelRegistry)+" (name text PRIMARY KEY, definition jsonb NOT NULL)"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func definitionJSON(d vectordata.CollectionDefinition) ([]byte, error) {
	d = d.Clone()
	for i := range d.Vectors {
		d.Vectors[i].Embedder = nil
	}
	return json.Marshal(d)
}
func (c *ModelCollection) validateSchema(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT a.attname, format_type(a.atttypid,a.atttypmod), a.attnotnull, a.attidentity::text FROM pg_attribute a JOIN pg_class t ON t.oid=a.attrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=$1 AND t.relname=$2 AND a.attnum>0 AND NOT a.attisdropped`, c.store.opts.Schema, c.name)
	if err != nil {
		return err
	}
	type col struct {
		typ      string
		required bool
		identity string
	}
	actual := map[string]col{}
	for rows.Next() {
		var name string
		var x col
		if err := rows.Scan(&name, &x.typ, &x.required, &x.identity); err != nil {
			rows.Close()
			return err
		}
		actual[name] = x
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	identity := ""
	if c.definition.Key.AutoGenerated {
		identity = "d"
	}
	want := map[string]col{c.definition.Key.StorageName: {keySQLType(c.definition.Key.Type), true, identity}}
	for _, p := range c.definition.Data {
		want[p.StorageName] = col{dataSQLType(p.Type), !p.Optional, ""}
	}
	for _, p := range c.definition.Vectors {
		want[p.StorageName] = col{vectorColumnType(p), true, ""}
	}
	if len(want) != len(actual) {
		return fmt.Errorf("%w: collection columns differ from definition", vectordata.ErrSchemaMismatch)
	}
	for name, x := range want {
		if actual[name] != x {
			return fmt.Errorf("%w: incompatible property %q", vectordata.ErrSchemaMismatch, name)
		}
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class t ON t.oid=i.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=i.indkey[0] WHERE n.nspname=$1 AND t.relname=$2 AND a.attname=$3 AND i.indisprimary AND i.indisvalid AND i.indimmediate AND i.indnkeyatts=1)`, c.store.opts.Schema, c.name, c.definition.Key.StorageName).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("%w: model key must be the non-deferrable primary key", vectordata.ErrSchemaMismatch)
	}
	return nil
}
func dataOpClass(t vectordata.DataType) string {
	switch t {
	case vectordata.TypeString:
		return "text_ops"
	case vectordata.TypeInt64:
		return "int8_ops"
	case vectordata.TypeFloat64:
		return "float8_ops"
	case vectordata.TypeBool:
		return "bool_ops"
	case vectordata.TypeTime:
		return "timestamptz_ops"
	case vectordata.TypeStringArray:
		return "array_ops"
	case vectordata.TypeUUID:
		return "uuid_ops"
	case vectordata.TypeBytes:
		return "bytea_ops"
	}
	return ""
}
func (c *ModelCollection) ensureModelIndex(ctx context.Context, tx pgx.Tx, name, method, column, opClass, with string, fullText bool) error {
	expression := quoteIdent(column) + " " + opClass
	if fullText {
		expression = ftsExpr(quoteIdent(column))
	}
	if _, err := tx.Exec(ctx, "CREATE INDEX IF NOT EXISTS "+quoteIdent(name)+" ON "+c.table()+" USING "+method+" ("+expression+")"+with); err != nil {
		return err
	}
	return c.validateModelIndex(ctx, tx, name, method, column, opClass, with, fullText)
}

// validateModelIndex is read-only and shared by EnsureExists and migration inspection.
func (c *ModelCollection) validateModelIndex(ctx context.Context, tx pgx.Tx, name, method, column, opClass, with string, fullText bool) error {
	var actualMethod, actualColumn, actualClass, actualExpression, canonicalColumn string
	var valid bool
	var options []string
	err := tx.QueryRow(ctx, `SELECT am.amname, coalesce(a.attname,''), oc.opcname, coalesce(pg_get_indexdef(i.indexrelid,1,true),''), quote_ident($3),
 i.indrelid=to_regclass($4) AND i.indisvalid AND i.indisready AND i.indnatts=1 AND i.indnkeyatts=1 AND i.indpred IS NULL,
 coalesce(idx.reloptions,ARRAY[]::text[])
 FROM pg_class idx JOIN pg_namespace n ON n.oid=idx.relnamespace JOIN pg_index i ON i.indexrelid=idx.oid
 JOIN pg_am am ON am.oid=idx.relam JOIN pg_opclass oc ON oc.oid=i.indclass[0]
 LEFT JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=i.indkey[0]
 WHERE n.nspname=$1 AND idx.relname=$2`, c.store.opts.Schema, name, column, c.table()).Scan(&actualMethod, &actualColumn, &actualClass, &actualExpression, &canonicalColumn, &valid, &options)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: name %q is not an index", vectordata.ErrSchemaMismatch, name)
	}
	if err != nil {
		return err
	}
	if fullText {
		expected := "to_tsvector('simple'::regconfig, COALESCE(" + canonicalColumn + ", ''::text))"
		valid = valid && actualColumn == "" && actualExpression == expected
	} else {
		valid = valid && actualColumn == column
	}
	if !valid || actualMethod != method || actualClass != opClass {
		return fmt.Errorf("%w: conflicting index %q", vectordata.ErrSchemaMismatch, name)
	}
	values := map[string]string{"m": "16", "ef_construction": "64", "lists": "100"}
	for _, option := range options {
		key, value, ok := strings.Cut(option, "=")
		if ok {
			values[key] = value
		}
	}
	for _, option := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(with, " WITH ("), ")"), ",") {
		key, value, ok := strings.Cut(option, "=")
		if ok && values[strings.TrimSpace(key)] != strings.TrimSpace(value) {
			return fmt.Errorf("%w: conflicting index parameter %q", vectordata.ErrSchemaMismatch, key)
		}
	}
	return nil
}
func ftsExpr(column string) string {
	return "to_tsvector('simple'::regconfig, coalesce(" + column + ", ''::text))"
}
