package postgres

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"github.com/gabisonia/go-vectorstore/stores/postgres/filtersql"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

var _ vectordata.RecordQueryer = (*PostgresCollection)(nil)
var _ vectordata.CollectionLifecycle = (*PostgresCollection)(nil)

func (c *PostgresCollection) Exists(ctx context.Context) (bool, error) {
	return c.store.CollectionExists(ctx, c.name)
}
func (c *PostgresCollection) EnsureExists(ctx context.Context) error {
	_, err := c.store.EnsureCollection(ctx, vectordata.CollectionSpec{Name: c.name, Dimension: c.dimension, Metric: c.metric})
	return err
}
func (c *PostgresCollection) EnsureDeleted(ctx context.Context) error {
	return c.store.DeleteCollection(ctx, c.name)
}
func (c *PostgresCollection) GetWithOptions(ctx context.Context, id string, opts vectordata.RetrievalOptions) (vectordata.Record, error) {
	records, err := c.GetMany(ctx, []string{id}, opts)
	if err != nil {
		return vectordata.Record{}, err
	}
	if len(records) == 0 {
		return vectordata.Record{}, vectordata.ErrNotFound
	}
	return records[0], nil
}
func (c *PostgresCollection) GetMany(ctx context.Context, ids []string, opts vectordata.RetrievalOptions) ([]vectordata.Record, error) {
	values := []any{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			values = append(values, id)
		}
	}
	out := []vectordata.Record{}
	for start := 0; start < len(values); start += maxRowsPerStatement {
		chunk := values[start:min(start+maxRowsPerStatement, len(values))]
		records, err := c.Query(ctx, vectordata.In(vectordata.Column(idColumn), chunk...), len(chunk), vectordata.QueryOptions{IncludeVectors: opts.IncludeVectors})
		if err != nil {
			return nil, err
		}
		out = append(out, records...)
	}
	return out, ctx.Err()
}
func (c *PostgresCollection) Query(ctx context.Context, filter vectordata.Filter, top int, opts vectordata.QueryOptions) ([]vectordata.Record, error) {
	out := []vectordata.Record{}
	for r, err := range c.QueryIter(ctx, filter, top, opts) {
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func (c *PostgresCollection) QueryIter(ctx context.Context, filter vectordata.Filter, top int, opts vectordata.QueryOptions) iter.Seq2[vectordata.Record, error] {
	return func(yield func(vectordata.Record, error) bool) {
		report := func(err error) { yield(vectordata.Record{}, err) }
		if top <= 0 || opts.Skip < 0 {
			report(vectordata.ErrInvalidSearchOptions)
			return
		}
		columns := []string{quoteIdent(idColumn)}
		if opts.IncludeVectors {
			columns = append(columns, quoteIdent(vectorColumn)+"::text")
		}
		columns = append(columns, quoteIdent(metadataColumn), quoteIdent(contentColumn), "0::float8")
		predicate, args, next, err := filtersql.CompileFilterSQL(filter, c.filterConfig(), 1)
		if err != nil {
			report(err)
			return
		}
		sql := "SELECT " + strings.Join(columns, ",") + " FROM " + c.tableName()
		if predicate != "" {
			sql += " WHERE " + predicate
		}
		order := []string{}
		cfg := c.filterConfig()
		for _, field := range opts.OrderBy {
			expr, ok := cfg.ColumnExpr[field.Property]
			if !ok {
				report(fmt.Errorf("%w: unknown sort property", vectordata.ErrInvalidFilter))
				return
			}
			direction := " ASC NULLS LAST"
			if field.Descending {
				direction = " DESC NULLS LAST"
			}
			order = append(order, expr+direction)
		}
		order = append(order, quoteIdent(idColumn)+" ASC")
		sql += " ORDER BY " + strings.Join(order, ",") + fmt.Sprintf(" LIMIT $%d OFFSET $%d", next, next+1)
		args = append(args, top, opts.Skip)
		rows, err := c.store.pool.Query(ctx, sql, args...)
		if err != nil {
			report(err)
			return
		}
		defer rows.Close()
		projection := vectordata.Projection{IncludeVector: opts.IncludeVectors, IncludeMetadata: true, IncludeContent: true}
		for rows.Next() {
			r, err := c.scanSearchResult(rows, projection)
			if err != nil {
				report(err)
				return
			}
			if !yield(r.Record, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			report(err)
		}
	}
}
