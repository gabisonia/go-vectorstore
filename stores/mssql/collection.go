package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type writeMode int

const (
	writeModeInsert writeMode = iota
	writeModeUpsert
)

// MSSQLCollection is a SQL Server-backed vector collection.
type MSSQLCollection struct {
	store     *MSSQLVectorStore
	name      string
	dimension int
	metric    vectordata.DistanceMetric
}

func (c *MSSQLCollection) Name() string {
	return c.name
}

func (c *MSSQLCollection) Dimension() int {
	return c.dimension
}

func (c *MSSQLCollection) Metric() vectordata.DistanceMetric {
	return c.metric
}

func (c *MSSQLCollection) Insert(ctx context.Context, records []vectordata.Record) error {
	return c.writeRecords(ctx, records, writeModeInsert)
}

func (c *MSSQLCollection) Upsert(ctx context.Context, records []vectordata.Record) error {
	return c.writeRecords(ctx, records, writeModeUpsert)
}

func (c *MSSQLCollection) Get(ctx context.Context, id string) (vectordata.Record, error) {
	query := fmt.Sprintf("SELECT %s, %s, %s, %s FROM %s WHERE %s = @p1",
		quoteIdent(idColumn),
		quoteIdent(vectorColumn),
		quoteIdent(metadataColumn),
		quoteIdent(contentColumn),
		c.tableName(),
		quoteIdent(idColumn),
	)

	var out vectordata.Record
	var vectorRaw string
	var metadataRaw string
	var content sql.NullString

	err := c.store.db.QueryRowContext(ctx, query, id).Scan(&out.ID, &vectorRaw, &metadataRaw, &content)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return vectordata.Record{}, vectordata.ErrNotFound
		}
		return vectordata.Record{}, err
	}

	vector, err := parseVectorJSON(vectorRaw)
	if err != nil {
		return vectordata.Record{}, fmt.Errorf("decode vector: %w", err)
	}
	metadata, err := parseMetadataJSON(metadataRaw)
	if err != nil {
		return vectordata.Record{}, fmt.Errorf("decode metadata: %w", err)
	}

	out.Vector = vector
	out.Metadata = metadata
	if content.Valid {
		value := content.String
		out.Content = &value
	}

	return out, nil
}

func (c *MSSQLCollection) Delete(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	args := make([]any, 0, len(ids))
	placeholders := make([]string, 0, len(ids))
	for i, id := range ids {
		placeholders = append(placeholders, fmt.Sprintf("@p%d", i+1))
		args = append(args, id)
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
		c.tableName(),
		quoteIdent(idColumn),
		strings.Join(placeholders, ", "),
	)
	result, err := c.store.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return rowsAffected, nil
}

func (c *MSSQLCollection) Count(ctx context.Context, filter vectordata.Filter) (int64, error) {
	if filter == nil {
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s", c.tableName())
		var count int64
		if err := c.store.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return 0, err
		}
		return count, nil
	}

	records, err := c.loadRecords(ctx, false)
	if err != nil {
		return 0, err
	}

	count := int64(0)
	for _, record := range records {
		matches, err := matchesFilter(filter, record)
		if err != nil {
			return 0, err
		}
		if matches {
			count++
		}
	}
	return count, nil
}

func (c *MSSQLCollection) SearchByVector(ctx context.Context, vector []float32, topK int, opts vectordata.SearchOptions) ([]vectordata.SearchResult, error) {
	if topK <= 0 {
		return nil, fmt.Errorf("topK must be > 0")
	}
	if err := c.validateVectorDimension(vector); err != nil {
		return nil, err
	}

	projection := resolveProjection(opts.Projection)
	metric := defaultMetric(c.metric)
	records, err := c.loadRecords(ctx, true)
	if err != nil {
		return nil, err
	}

	results := make([]vectordata.SearchResult, 0, len(records))
	for _, record := range records {
		if err := c.validateVectorDimension(record.Vector); err != nil {
			return nil, fmt.Errorf("invalid stored vector for record %q: %w", record.ID, err)
		}

		matches, err := matchesFilter(opts.Filter, record)
		if err != nil {
			return nil, err
		}
		if !matches {
			continue
		}

		distance, err := distanceBetween(metric, vector, record.Vector)
		if err != nil {
			return nil, err
		}
		if opts.Threshold != nil && distance > *opts.Threshold {
			continue
		}

		results = append(results, vectordata.SearchResult{
			Record:   projectRecord(record, projection),
			Distance: distance,
			Score:    vectordata.ScoreFromDistance(metric, distance),
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Distance == results[j].Distance {
			return results[i].Record.ID < results[j].Record.ID
		}
		return results[i].Distance < results[j].Distance
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

func (c *MSSQLCollection) EnsureIndexes(ctx context.Context, opts vectordata.IndexOptions) error {
	// SQL Server backend stores vectors as JSON payloads in this MVP, so index options are no-op.
	_ = ctx
	_ = opts
	return nil
}

func (c *MSSQLCollection) writeRecords(ctx context.Context, records []vectordata.Record, mode writeMode) error {
	if len(records) == 0 {
		return nil
	}

	insertQuery := fmt.Sprintf("INSERT INTO %s (%s, %s, %s, %s) VALUES (@p1, @p2, @p3, @p4)",
		c.tableName(),
		quoteIdent(idColumn),
		quoteIdent(vectorColumn),
		quoteIdent(metadataColumn),
		quoteIdent(contentColumn),
	)
	updateQuery := fmt.Sprintf("UPDATE %s SET %s = @p2, %s = @p3, %s = @p4 WHERE %s = @p1",
		c.tableName(),
		quoteIdent(vectorColumn),
		quoteIdent(metadataColumn),
		quoteIdent(contentColumn),
		quoteIdent(idColumn),
	)

	for start := 0; start < len(records); start += maxRowsPerStatement {
		end := start + maxRowsPerStatement
		if end > len(records) {
			end = len(records)
		}

		tx, err := c.store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := c.writeBatch(ctx, tx, records[start:end], mode, insertQuery, updateQuery); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	return nil
}

func (c *MSSQLCollection) writeBatch(
	ctx context.Context,
	tx *sql.Tx,
	records []vectordata.Record,
	mode writeMode,
	insertQuery string,
	updateQuery string,
) error {
	for _, record := range records {
		if strings.TrimSpace(record.ID) == "" {
			return fmt.Errorf("record id is empty")
		}
		if err := c.validateVectorDimension(record.Vector); err != nil {
			return err
		}

		vectorPayload, err := vectorJSON(record.Vector)
		if err != nil {
			return fmt.Errorf("encode vector for record %q: %w", record.ID, err)
		}
		metadataPayload, err := metadataJSON(record.Metadata)
		if err != nil {
			return fmt.Errorf("encode metadata for record %q: %w", record.ID, err)
		}

		var contentArg any
		if record.Content != nil {
			contentArg = *record.Content
		}

		switch mode {
		case writeModeInsert:
			if _, err := tx.ExecContext(ctx, insertQuery, record.ID, vectorPayload, metadataPayload, contentArg); err != nil {
				return err
			}
		case writeModeUpsert:
			result, err := tx.ExecContext(ctx, updateQuery, record.ID, vectorPayload, metadataPayload, contentArg)
			if err != nil {
				return err
			}

			rowsAffected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if rowsAffected == 0 {
				if _, err := tx.ExecContext(ctx, insertQuery, record.ID, vectorPayload, metadataPayload, contentArg); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unsupported write mode %d", mode)
		}
	}

	return nil
}

func (c *MSSQLCollection) loadRecords(ctx context.Context, includeVector bool) ([]vectordata.Record, error) {
	selectColumns := []string{quoteIdent(idColumn)}
	if includeVector {
		selectColumns = append(selectColumns, quoteIdent(vectorColumn))
	}
	selectColumns = append(selectColumns, quoteIdent(metadataColumn), quoteIdent(contentColumn))

	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectColumns, ", "), c.tableName())
	rows, err := c.store.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]vectordata.Record, 0)
	for rows.Next() {
		var record vectordata.Record
		var vectorRaw string
		var metadataRaw string
		var content sql.NullString

		if includeVector {
			if err := rows.Scan(&record.ID, &vectorRaw, &metadataRaw, &content); err != nil {
				return nil, err
			}
			parsedVector, err := parseVectorJSON(vectorRaw)
			if err != nil {
				return nil, fmt.Errorf("decode vector: %w", err)
			}
			record.Vector = parsedVector
		} else {
			if err := rows.Scan(&record.ID, &metadataRaw, &content); err != nil {
				return nil, err
			}
		}

		metadata, err := parseMetadataJSON(metadataRaw)
		if err != nil {
			return nil, fmt.Errorf("decode metadata: %w", err)
		}
		record.Metadata = metadata

		if content.Valid {
			value := content.String
			record.Content = &value
		}

		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return records, nil
}

func (c *MSSQLCollection) validateVectorDimension(vector []float32) error {
	if len(vector) != c.dimension {
		return fmt.Errorf("%w: expected %d, got %d", vectordata.ErrDimensionMismatch, c.dimension, len(vector))
	}
	return nil
}

func (c *MSSQLCollection) tableName() string {
	return qualifiedTable(c.store.opts.Schema, c.name)
}

func projectRecord(record vectordata.Record, projection vectordata.Projection) vectordata.Record {
	projected := vectordata.Record{ID: record.ID}
	if projection.IncludeVector {
		projected.Vector = append([]float32(nil), record.Vector...)
	}
	if projection.IncludeMetadata {
		metadataCopy := make(map[string]any, len(record.Metadata))
		for key, value := range record.Metadata {
			metadataCopy[key] = value
		}
		projected.Metadata = metadataCopy
	}
	if projection.IncludeContent && record.Content != nil {
		contentCopy := *record.Content
		projected.Content = &contentCopy
	}
	return projected
}
