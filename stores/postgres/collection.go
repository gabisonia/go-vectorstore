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
		return searchPlan{}, fmt.Errorf("topK must be > 0")
	}
	if err := c.validateVectorDimension(vector); err != nil {
		return searchPlan{}, err
	}

	operator, err := metricOperator(defaultMetric(c.metric))
	if err != nil {
		return searchPlan{}, err
	}
	distanceExpr := fmt.Sprintf(`%s %s $1::vector`, quoteIdent(vectorColumn), operator)
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

	return searchPlan{
		query:      b.String(),
		args:       args,
		projection: projection,
	}, nil
}

func (c *PostgresCollection) executeSearchPlan(ctx context.Context, plan searchPlan) ([]vectordata.SearchResult, error) {
	rows, err := c.store.pool.Query(ctx, plan.query, plan.args...)
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

	for start := 0; start < len(records); start += maxRowsPerStatement {
		end := start + maxRowsPerStatement
		if end > len(records) {
			end = len(records)
		}

		query, args, err := c.buildWriteBatch(records[start:end], mode)
		if err != nil {
			return err
		}
		if _, err := c.store.pool.Exec(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}

func (c *PostgresCollection) buildWriteBatch(records []vectordata.Record, mode writeMode) (string, []any, error) {
	args := make([]any, 0, len(records)*4)
	values := make([]string, 0, len(records))

	for i, record := range records {
		if strings.TrimSpace(record.ID) == "" {
			return "", nil, fmt.Errorf("record id is empty")
		}
		if err := c.validateVectorDimension(record.Vector); err != nil {
			return "", nil, err
		}

		metadataPayload, err := metadataJSON(record.Metadata)
		if err != nil {
			return "", nil, fmt.Errorf("encode metadata for record %q: %w", record.ID, err)
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

func (c *PostgresCollection) ensureVectorIndex(ctx context.Context, opts *vectordata.VectorIndexOptions) error {
	method := vectordata.IndexMethodHNSW
	if opts.Method != "" {
		method = opts.Method
	}

	metric := defaultMetric(c.metric)
	if opts.Metric != "" {
		metric = opts.Metric
	}

	opClass, err := metricOpClass(metric)
	if err != nil {
		return err
	}

	indexName := opts.Name
	if indexName == "" {
		indexName = fmt.Sprintf("idx_%s_vector_%s", c.name, method)
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
	return nil
}

func (c *PostgresCollection) ensureMetadataIndex(ctx context.Context, opts *vectordata.MetadataIndexOptions) error {
	indexName := opts.Name
	if indexName == "" {
		indexName = fmt.Sprintf("idx_%s_metadata_gin", c.name)
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
	return nil
}

func (c *PostgresCollection) filterConfig() vectordata.FilterSQLConfig {
	return vectordata.FilterSQLConfig{
		ColumnExpr: map[string]string{
			idColumn:      quoteIdent(idColumn),
			contentColumn: quoteIdent(contentColumn),
		},
		MetadataExpr: quoteIdent(metadataColumn),
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
		if m == 0 {
			m = 16
		}
		if ef == 0 {
			ef = 64
		}
		return fmt.Sprintf(" WITH (m = %d, ef_construction = %d)", m, ef), nil
	case vectordata.IndexMethodIVFFlat:
		lists := opts.IVFFlat.Lists
		if lists == 0 {
			lists = 100
		}
		return fmt.Sprintf(" WITH (lists = %d)", lists), nil
	default:
		return "", fmt.Errorf("%w: unsupported index method %q", vectordata.ErrSchemaMismatch, method)
	}
}
