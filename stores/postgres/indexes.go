package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

func (c *PostgresCollection) ensureVectorIndex(ctx context.Context, opts *vectordata.VectorIndexOptions) error {
	method := vectordata.IndexMethodHNSW
	if opts.Method != "" {
		method = opts.Method
	}

	metric := defaultMetric(c.metric)
	if opts.Metric != "" {
		if opts.Metric != metric {
			return fmt.Errorf("%w: index metric %q differs from collection metric %q", vectordata.ErrSchemaMismatch, opts.Metric, metric)
		}
		metric = opts.Metric
	}

	opClass, err := metricOpClass(metric)
	if err != nil {
		return err
	}

	indexName, err := resolveIndexName(opts.Name, fmt.Sprintf("idx_%s_vector_%s", c.name, method))
	if err != nil {
		return err
	}

	withClause, err := buildVectorIndexWithClause(method, opts)
	if err != nil {
		return err
	}

	query := fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS %s ON %s USING %s (%s %s)%s",
		quoteIdent(indexName),
		c.tableName(),
		method,
		quoteIdent(vectorColumn),
		opClass,
		withClause,
	)
	if _, err := c.store.pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("ensure vector index: %w", err)
	}
	parameters := map[string]int{}
	switch method {
	case vectordata.IndexMethodHNSW:
		m, ef := opts.HNSW.M, opts.HNSW.EfConstruction
		if m == 0 {
			m = 16
		}
		if ef == 0 {
			ef = 64
		}
		parameters["m"], parameters["ef_construction"] = m, ef
	case vectordata.IndexMethodIVFFlat:
		lists := opts.IVFFlat.Lists
		if lists == 0 {
			lists = 100
		}
		parameters["lists"] = lists
	}
	return c.validateIndex(ctx, indexName, string(method), vectorColumn, opClass, parameters)
}

func (c *PostgresCollection) ensureMetadataIndex(ctx context.Context, opts *vectordata.MetadataIndexOptions) error {
	indexName, err := resolveIndexName(opts.Name, fmt.Sprintf("idx_%s_metadata_gin", c.name))
	if err != nil {
		return err
	}

	metadataExpr := quoteIdent(metadataColumn)
	if opts.UsePathOps {
		metadataExpr += " jsonb_path_ops"
	}

	query := fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS %s ON %s USING gin (%s)",
		quoteIdent(indexName),
		c.tableName(),
		metadataExpr,
	)
	if _, err := c.store.pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("ensure metadata index: %w", err)
	}
	opClass := "jsonb_ops"
	if opts.UsePathOps {
		opClass = "jsonb_path_ops"
	}
	return c.validateIndex(ctx, indexName, "gin", metadataColumn, opClass, nil)
}

func resolveIndexName(requested, generated string) (string, error) {
	// PostgreSQL silently truncates identifiers to 63 bytes. Hash long generated
	// names to keep validation reliable and prevent collisions between indexes.
	if requested != "" {
		if len(requested) > 63 {
			return "", fmt.Errorf("%w: index name exceeds 63 bytes", vectordata.ErrSchemaMismatch)
		}
		return requested, nil
	}
	if len(generated) <= 63 {
		return generated, nil
	}
	sum := sha256.Sum256([]byte(generated))
	prefix := generated[:54]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return fmt.Sprintf("%s_%x", prefix, sum[:4]), nil
}

// CREATE INDEX IF NOT EXISTS only checks the name. Verify the actual index,
// including its table, expression, predicate, operator class and build options.
func (c *PostgresCollection) validateIndex(ctx context.Context, name, method, column, opClass string, parameters map[string]int) error {
	var actualMethod, tableSchema, table, actualColumn, actualOpClass string
	var usable bool
	var options []string
	err := c.store.pool.QueryRow(ctx, `
		SELECT am.amname, tn.nspname, t.relname, COALESCE(a.attname, ''), oc.opcname,
			i.indisvalid AND i.indisready AND i.indpred IS NULL AND i.indexprs IS NULL
			AND i.indnatts = 1 AND i.indnkeyatts = 1,
			COALESCE(idx.reloptions, ARRAY[]::text[])
		FROM pg_class idx
		JOIN pg_namespace n ON n.oid = idx.relnamespace
		JOIN pg_index i ON i.indexrelid = idx.oid
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace tn ON tn.oid = t.relnamespace
		JOIN pg_am am ON am.oid = idx.relam
		JOIN pg_opclass oc ON oc.oid = i.indclass[0]
		LEFT JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = i.indkey[0]
		WHERE n.nspname = $1 AND idx.relname = $2`, c.store.opts.Schema, name).
		Scan(&actualMethod, &tableSchema, &table, &actualColumn, &actualOpClass, &usable, &options)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %q is not an index", vectordata.ErrSchemaMismatch, name)
	}
	if err != nil {
		return fmt.Errorf("inspect index %q: %w", name, err)
	}
	if !usable || tableSchema != c.store.opts.Schema || table != c.name || actualMethod != method || actualColumn != column || actualOpClass != opClass {
		return fmt.Errorf("%w: index %q does not match requested %s index on %s (%s %s)", vectordata.ErrSchemaMismatch, name, method, c.tableName(), column, opClass)
	}
	defaults := map[string]int{"m": 16, "ef_construction": 64, "lists": 100}
	for _, option := range options {
		key, value, ok := strings.Cut(option, "=")
		if !ok {
			continue
		}
		if _, relevant := parameters[key]; !relevant {
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("inspect index %q option %q: %w", name, key, err)
		}
		defaults[key] = n
	}
	for key, want := range parameters {
		if defaults[key] != want {
			return fmt.Errorf("%w: index %q has %s=%d, expected %d", vectordata.ErrSchemaMismatch, name, key, defaults[key], want)
		}
	}
	return nil
}
