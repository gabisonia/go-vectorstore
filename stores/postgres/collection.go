package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

const maxRowsPerStatement = 500

type writeMode int

const (
	writeModeInsert writeMode = iota
	writeModeUpsert
)

type searchPlan struct {
	query      string
	args       []any
	projection vectordata.Projection
	settings   []searchSetting
	iterative  bool
}

// PostgresCollection is a PostgreSQL-backed vector collection.
type PostgresCollection struct {
	store     *PostgresVectorStore
	name      string
	dimension int
	metric    vectordata.DistanceMetric
}

func (c *PostgresCollection) Name() string {
	return c.name
}

func (c *PostgresCollection) Dimension() int {
	return c.dimension
}

func (c *PostgresCollection) Metric() vectordata.DistanceMetric {
	return c.metric
}

func (c *PostgresCollection) Insert(ctx context.Context, records []vectordata.Record) error {
	return c.writeRecords(ctx, records, writeModeInsert)
}

func (c *PostgresCollection) Upsert(ctx context.Context, records []vectordata.Record) error {
	return c.writeRecords(ctx, records, writeModeUpsert)
}

func (c *PostgresCollection) Get(ctx context.Context, id string) (vectordata.Record, error) {
	query := fmt.Sprintf(`
		SELECT %s, %s::text, %s, %s
		FROM %s
		WHERE %s = $1
	`,
		quoteIdent(idColumn),
		quoteIdent(vectorColumn),
		quoteIdent(metadataColumn),
		quoteIdent(contentColumn),
		c.tableName(),
		quoteIdent(idColumn),
	)

	var out vectordata.Record
	var vectorText string
	var metadataRaw []byte
	if err := c.store.pool.QueryRow(ctx, query, id).Scan(&out.ID, &vectorText, &metadataRaw, &out.Content); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return vectordata.Record{}, vectordata.ErrNotFound
		}
		return vectordata.Record{}, err
	}

	vector, err := parseVectorText(vectorText)
	if err != nil {
		return vectordata.Record{}, fmt.Errorf("decode vector: %w", err)
	}
	metadata, err := parseMetadata(metadataRaw)
	if err != nil {
		return vectordata.Record{}, fmt.Errorf("decode metadata: %w", err)
	}
	out.Vector = vector
	out.Metadata = metadata

	return out, nil
}

func (c *PostgresCollection) Delete(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	query := fmt.Sprintf(`DELETE FROM %s WHERE %s = ANY($1)`, c.tableName(), quoteIdent(idColumn))
	cmd, err := c.store.pool.Exec(ctx, query, ids)
	if err != nil {
		return 0, err
	}
	return cmd.RowsAffected(), nil
}

func (c *PostgresCollection) Count(ctx context.Context, filter vectordata.Filter) (int64, error) {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, c.tableName())
	whereSQL, args, _, err := vectordata.CompileFilterSQL(filter, c.filterConfig(), 1)
	if err != nil {
		return 0, err
	}
	if whereSQL != "" {
		query += " WHERE " + whereSQL
	}

	var count int64
	if err := c.store.pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (c *PostgresCollection) SearchByVector(ctx context.Context, vector []float32, topK int, opts vectordata.SearchOptions) ([]vectordata.SearchResult, error) {
	plan, err := c.buildSearchPlan(vector, topK, opts)
	if err != nil {
		return nil, err
	}
	return c.executeSearchPlan(ctx, plan)
}

func (c *PostgresCollection) EnsureIndexes(ctx context.Context, opts vectordata.IndexOptions) error {
	if opts.Vector != nil {
		if err := c.ensureVectorIndex(ctx, opts.Vector); err != nil {
			return err
		}
	}
	if opts.Metadata != nil {
		if err := c.ensureMetadataIndex(ctx, opts.Metadata); err != nil {
			return err
		}
	}
	return nil
}

func (c *PostgresCollection) buildSearchPlan(vector []float32, topK int, opts vectordata.SearchOptions) (searchPlan, error) {
	if topK <= 0 {
		return searchPlan{}, fmt.Errorf("%w: topK must be > 0", vectordata.ErrInvalidSearchOptions)
	}
	if err := c.validateVectorDimension(vector); err != nil {
		return searchPlan{}, err
	}
	settings, iterative, err := searchSettings(opts)
	if err != nil {
		return searchPlan{}, err
	}

	operator, err := metricOperator(defaultMetric(c.metric))
	if err != nil {
		return searchPlan{}, err
	}
	distanceExpr := fmt.Sprintf(`(%s %s $1::vector)`, quoteIdent(vectorColumn), operator)
	projection := resolveProjection(opts.Projection)

	selectCols := []string{quoteIdent(idColumn)}
	if projection.IncludeVector {
		selectCols = append(selectCols, quoteIdent(vectorColumn)+"::text")
	}
	if projection.IncludeMetadata {
		selectCols = append(selectCols, quoteIdent(metadataColumn))
	}
	if projection.IncludeContent {
		selectCols = append(selectCols, quoteIdent(contentColumn))
	}
	selectCols = append(selectCols, distanceExpr+" AS distance")

	args := []any{vectorLiteral(vector)}
	nextArg := 2
	whereParts := make([]string, 0, 2)

	if opts.Filter != nil {
		whereSQL, filterArgs, next, err := vectordata.CompileFilterSQL(opts.Filter, c.filterConfig(), nextArg)
		if err != nil {
			return searchPlan{}, err
		}
		if whereSQL != "" {
			whereParts = append(whereParts, whereSQL)
		}
		args = append(args, filterArgs...)
		nextArg = next
	}

	if opts.Threshold != nil {
		whereParts = append(whereParts, fmt.Sprintf("(%s <= $%d)", distanceExpr, nextArg))
		args = append(args, *opts.Threshold)
		nextArg++
	}

	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(strings.Join(selectCols, ", "))
	b.WriteString(" FROM ")
	b.WriteString(c.tableName())
	if len(whereParts) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(whereParts, " AND "))
	}
	b.WriteString(" ORDER BY distance ASC")
	b.WriteString(fmt.Sprintf(" LIMIT $%d", nextArg))
	args = append(args, topK)

	query := b.String()
	if opts.IVFFlat != nil && opts.IVFFlat.IterativeScan {
		// IVFFlat only supports relaxed iterative ordering. Re-sort the
		// candidate set to preserve SearchByVector's ascending-distance order.
		query = "SELECT * FROM (" + query + ") AS nearest ORDER BY distance + 0 ASC"
	}
	return searchPlan{
		query:      query,
		args:       args,
		projection: projection,
		settings:   settings,
		iterative:  iterative,
	}, nil
}

func (c *PostgresCollection) executeSearchPlan(ctx context.Context, plan searchPlan) ([]vectordata.SearchResult, error) {
	if len(plan.settings) == 0 {
		return c.querySearchPlan(ctx, c.store.pool, plan)
	}
	tx, err := c.store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackTx(ctx, tx)
	if plan.iterative {
		var version string
		if err := tx.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'vector'").Scan(&version); err != nil {
			return nil, err
		}
		if !supportsIterativeScans(version) {
			return nil, fmt.Errorf("%w: iterative scans require pgvector 0.8+, found %s", vectordata.ErrInvalidSearchOptions, version)
		}
	}
	for _, setting := range plan.settings {
		if _, err := tx.Exec(ctx, "SELECT pg_catalog.set_config($1, $2, true)", setting.name, setting.value); err != nil {
			return nil, fmt.Errorf("set search option %s: %w", setting.name, err)
		}
	}
	results, err := c.querySearchPlan(ctx, tx, plan)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return results, nil
}

type searchQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (c *PostgresCollection) querySearchPlan(ctx context.Context, conn searchQueryer, plan searchPlan) ([]vectordata.SearchResult, error) {
	rows, err := conn.Query(ctx, plan.query, plan.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]vectordata.SearchResult, 0)
	for rows.Next() {
		result, err := c.scanSearchResult(rows, plan.projection)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (c *PostgresCollection) scanSearchResult(rows pgx.Rows, projection vectordata.Projection) (vectordata.SearchResult, error) {
	var rec vectordata.Record
	var vectorText string
	var metadataRaw []byte
	var content *string
	var distance float64

	scanTargets := []any{&rec.ID}
	if projection.IncludeVector {
		scanTargets = append(scanTargets, &vectorText)
	}
	if projection.IncludeMetadata {
		scanTargets = append(scanTargets, &metadataRaw)
	}
	if projection.IncludeContent {
		scanTargets = append(scanTargets, &content)
	}
	scanTargets = append(scanTargets, &distance)

	if err := rows.Scan(scanTargets...); err != nil {
		return vectordata.SearchResult{}, err
	}

	if projection.IncludeVector {
		parsed, err := parseVectorText(vectorText)
		if err != nil {
			return vectordata.SearchResult{}, fmt.Errorf("decode vector: %w", err)
		}
		rec.Vector = parsed
	}
	if projection.IncludeMetadata {
		parsed, err := parseMetadata(metadataRaw)
		if err != nil {
			return vectordata.SearchResult{}, fmt.Errorf("decode metadata: %w", err)
		}
		rec.Metadata = parsed
	}
	if projection.IncludeContent {
		rec.Content = content
	}

	return vectordata.SearchResult{
		Record:   rec,
		Distance: distance,
		Score:    vectordata.ScoreFromDistance(defaultMetric(c.metric), distance),
	}, nil
}

func (c *PostgresCollection) writeRecords(ctx context.Context, records []vectordata.Record, mode writeMode) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := c.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	// Encode and execute one batch at a time so temporary memory does not
	// grow with the entire input. A later error still rolls back every batch.
	for start := 0; start < len(records); start += maxRowsPerStatement {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := start + maxRowsPerStatement
		if end > len(records) {
			end = len(records)
		}

		query, args, err := c.buildWriteBatch(records[start:end], mode)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (c *PostgresCollection) buildWriteBatch(records []vectordata.Record, mode writeMode) (string, []any, error) {
	args := make([]any, 0, len(records)*4)
	values := make([]string, 0, len(records))

	for i, record := range records {
		if strings.TrimSpace(record.ID) == "" {
			return "", nil, fmt.Errorf("%w: record id is empty", vectordata.ErrInvalidRecord)
		}
		if err := c.validateVectorDimension(record.Vector); err != nil {
			return "", nil, err
		}

		metadataPayload, err := metadataJSON(record.Metadata)
		if err != nil {
			return "", nil, fmt.Errorf("%w: encode metadata for record %q: %v", vectordata.ErrInvalidRecord, record.ID, err)
		}

		base := i*4 + 1
		values = append(values, fmt.Sprintf("($%d, $%d::vector, $%d::jsonb, $%d)", base, base+1, base+2, base+3))
		args = append(args, record.ID, vectorLiteral(record.Vector), metadataPayload, record.Content)
	}

	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(c.tableName())
	b.WriteString(" (")
	b.WriteString(strings.Join([]string{
		quoteIdent(idColumn),
		quoteIdent(vectorColumn),
		quoteIdent(metadataColumn),
		quoteIdent(contentColumn),
	}, ", "))
	b.WriteString(") VALUES ")
	b.WriteString(strings.Join(values, ", "))

	if mode == writeModeUpsert {
		b.WriteString(" ON CONFLICT (")
		b.WriteString(quoteIdent(idColumn))
		b.WriteString(") DO UPDATE SET ")
		b.WriteString(quoteIdent(vectorColumn) + " = EXCLUDED." + quoteIdent(vectorColumn) + ", ")
		b.WriteString(quoteIdent(metadataColumn) + " = EXCLUDED." + quoteIdent(metadataColumn) + ", ")
		b.WriteString(quoteIdent(contentColumn) + " = EXCLUDED." + quoteIdent(contentColumn))
	}

	return b.String(), args, nil
}

func (c *PostgresCollection) filterConfig() vectordata.FilterSQLConfig {
	return vectordata.FilterSQLConfig{
		ColumnExpr: map[string]string{
			idColumn:      quoteIdent(idColumn),
			contentColumn: quoteIdent(contentColumn),
		},
		MetadataExpr:           quoteIdent(metadataColumn),
		UseMetadataContainment: true,
	}
}

func (c *PostgresCollection) validateVectorDimension(vector []float32) error {
	if len(vector) != c.dimension {
		return fmt.Errorf("%w: expected %d, got %d", vectordata.ErrDimensionMismatch, c.dimension, len(vector))
	}
	return nil
}

func (c *PostgresCollection) tableName() string {
	return qualifiedTable(c.store.opts.Schema, c.name)
}

func resolveProjection(projection *vectordata.Projection) vectordata.Projection {
	if projection == nil {
		return vectordata.DefaultProjection()
	}
	return *projection
}

func buildVectorIndexWithClause(method vectordata.IndexMethod, opts *vectordata.VectorIndexOptions) (string, error) {
	switch method {
	case vectordata.IndexMethodHNSW:
		m := opts.HNSW.M
		ef := opts.HNSW.EfConstruction
		if m != 0 && m < 2 {
			return "", fmt.Errorf("%w: HNSW m must be 0 or >= 2", vectordata.ErrInvalidSearchOptions)
		}
		if ef != 0 && ef < 4 {
			return "", fmt.Errorf("%w: HNSW ef_construction must be 0 or >= 4", vectordata.ErrInvalidSearchOptions)
		}
		if m == 0 {
			m = 16
		}
		if ef == 0 {
			ef = 64
		}
		return fmt.Sprintf(" WITH (m = %d, ef_construction = %d)", m, ef), nil
	case vectordata.IndexMethodIVFFlat:
		lists := opts.IVFFlat.Lists
		if lists < 0 {
			return "", fmt.Errorf("%w: IVFFlat lists must be >= 0", vectordata.ErrInvalidSearchOptions)
		}
		if lists == 0 {
			lists = 100
		}
		return fmt.Sprintf(" WITH (lists = %d)", lists), nil
	default:
		return "", fmt.Errorf("%w: unsupported index method %q", vectordata.ErrSchemaMismatch, method)
	}
}
