package mssql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func (s *MSSQLVectorStore) ensureBaseSchema(ctx context.Context) error {
	schemaLiteral := escapeSQLString(s.opts.Schema)
	query := fmt.Sprintf("IF SCHEMA_ID(N'%s') IS NULL EXEC(N'CREATE SCHEMA %s')", schemaLiteral, quoteIdent(s.opts.Schema))
	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("ensure schema %q: %w", s.opts.Schema, err)
	}

	if err := s.ensureCollectionsMetadataTable(ctx); err != nil {
		return err
	}

	return nil
}

func (s *MSSQLVectorStore) ensureCollectionsMetadataTable(ctx context.Context) error {
	query := fmt.Sprintf(`
		IF OBJECT_ID(N'%s', N'U') IS NULL
		BEGIN
			CREATE TABLE %s (
				%s NVARCHAR(255) NOT NULL PRIMARY KEY,
				%s INT NOT NULL,
				%s NVARCHAR(64) NOT NULL
			)
		END
	`,
		escapeSQLString(objectIDName(s.opts.Schema, collectionMetaTable)),
		qualifiedTable(s.opts.Schema, collectionMetaTable),
		quoteIdent("name"),
		quoteIdent("dimension"),
		quoteIdent("metric"),
	)

	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("ensure collection metadata table: %w", err)
	}
	return nil
}

func (s *MSSQLVectorStore) ensureTableWithValidation(ctx context.Context, spec vectordata.CollectionSpec, mode vectordata.EnsureMode) error {
	exists, err := s.tableExists(ctx, spec.Name)
	if err != nil {
		return err
	}

	if !exists {
		if err := s.createCollectionTable(ctx, spec.Name); err != nil {
			return err
		}
		if err := s.upsertCollectionMetadata(ctx, spec.Name, spec.Dimension, spec.Metric); err != nil {
			return err
		}
		return nil
	}

	if err := s.validateCollectionSchema(ctx, spec.Name, mode); err != nil {
		return err
	}

	dimension, metric, found, err := s.readCollectionMetadata(ctx, spec.Name)
	if err != nil {
		return err
	}
	if !found {
		if mode == vectordata.EnsureStrict {
			return fmt.Errorf("%w: missing collection metadata for %q", vectordata.ErrSchemaMismatch, spec.Name)
		}
		return s.upsertCollectionMetadata(ctx, spec.Name, spec.Dimension, spec.Metric)
	}

	if dimension != spec.Dimension {
		return fmt.Errorf("%w: expected vector dimension %d, got %d", vectordata.ErrSchemaMismatch, spec.Dimension, dimension)
	}

	if defaultMetric(metric) != defaultMetric(spec.Metric) {
		return fmt.Errorf("%w: expected metric %q, got %q", vectordata.ErrSchemaMismatch, spec.Metric, metric)
	}

	return nil
}

func (s *MSSQLVectorStore) tableExists(ctx context.Context, table string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM INFORMATION_SCHEMA.TABLES
		WHERE TABLE_SCHEMA = @p1 AND TABLE_NAME = @p2
	`, s.opts.Schema, table).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check table exists: %w", err)
	}

	return count > 0, nil
}

func (s *MSSQLVectorStore) createCollectionTable(ctx context.Context, table string) error {
	query := fmt.Sprintf(`
		IF OBJECT_ID(N'%s', N'U') IS NULL
		BEGIN
			CREATE TABLE %s (
				%s NVARCHAR(255) NOT NULL PRIMARY KEY,
				%s NVARCHAR(MAX) NOT NULL,
				%s NVARCHAR(MAX) NOT NULL DEFAULT N'{}',
				%s NVARCHAR(MAX) NULL
			)
		END
	`,
		escapeSQLString(objectIDName(s.opts.Schema, table)),
		qualifiedTable(s.opts.Schema, table),
		quoteIdent(idColumn),
		quoteIdent(vectorColumn),
		quoteIdent(metadataColumn),
		quoteIdent(contentColumn),
	)

	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("create collection table %q: %w", table, err)
	}
	return nil
}

func (s *MSSQLVectorStore) validateCollectionSchema(ctx context.Context, table string, mode vectordata.EnsureMode) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, DATA_TYPE
		FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = @p1 AND TABLE_NAME = @p2
	`, s.opts.Schema, table)
	if err != nil {
		return fmt.Errorf("read schema columns: %w", err)
	}
	defer rows.Close()

	columns := make(map[string]string)
	for rows.Next() {
		var columnName string
		var dataType string
		if err := rows.Scan(&columnName, &dataType); err != nil {
			return fmt.Errorf("scan schema columns: %w", err)
		}
		columns[columnName] = dataType
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema columns: %w", err)
	}

	idType, ok := columns[idColumn]
	if !ok {
		return fmt.Errorf("%w: missing column %q", vectordata.ErrSchemaMismatch, idColumn)
	}
	if !isStringType(idType) {
		return fmt.Errorf("%w: expected %q to be string-compatible type, got %q", vectordata.ErrSchemaMismatch, idColumn, idType)
	}

	vectorType, ok := columns[vectorColumn]
	if !ok {
		return fmt.Errorf("%w: missing column %q", vectordata.ErrSchemaMismatch, vectorColumn)
	}
	if !isStringType(vectorType) {
		return fmt.Errorf("%w: expected %q to be string-compatible type, got %q", vectordata.ErrSchemaMismatch, vectorColumn, vectorType)
	}

	if err := s.ensurePrimaryKeyOnID(ctx, table); err != nil {
		return err
	}

	metadataType, hasMetadata := columns[metadataColumn]
	if !hasMetadata {
		if mode == vectordata.EnsureStrict {
			return fmt.Errorf("%w: missing column %q", vectordata.ErrSchemaMismatch, metadataColumn)
		}
		if err := s.addMetadataColumn(ctx, table); err != nil {
			return err
		}
	} else if !isStringType(metadataType) {
		return fmt.Errorf("%w: expected %q to be string-compatible type, got %q", vectordata.ErrSchemaMismatch, metadataColumn, metadataType)
	}

	contentType, hasContent := columns[contentColumn]
	if !hasContent {
		if mode == vectordata.EnsureStrict {
			return fmt.Errorf("%w: missing column %q", vectordata.ErrSchemaMismatch, contentColumn)
		}
		if err := s.addContentColumn(ctx, table); err != nil {
			return err
		}
	} else if !isStringType(contentType) {
		return fmt.Errorf("%w: expected %q to be string-compatible type, got %q", vectordata.ErrSchemaMismatch, contentColumn, contentType)
	}

	return nil
}

func (s *MSSQLVectorStore) ensurePrimaryKeyOnID(ctx context.Context, table string) error {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
		INNER JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
			ON tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
			AND tc.TABLE_SCHEMA = kcu.TABLE_SCHEMA
			AND tc.TABLE_NAME = kcu.TABLE_NAME
		WHERE tc.TABLE_SCHEMA = @p1
			AND tc.TABLE_NAME = @p2
			AND tc.CONSTRAINT_TYPE = 'PRIMARY KEY'
			AND kcu.COLUMN_NAME = @p3
	`, s.opts.Schema, table, idColumn).Scan(&count)
	if err != nil {
		return fmt.Errorf("check primary key: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("%w: primary key on %q is required", vectordata.ErrSchemaMismatch, idColumn)
	}
	return nil
}

func (s *MSSQLVectorStore) addMetadataColumn(ctx context.Context, table string) error {
	query := fmt.Sprintf("ALTER TABLE %s ADD %s NVARCHAR(MAX) NOT NULL DEFAULT N'{}'", qualifiedTable(s.opts.Schema, table), quoteIdent(metadataColumn))
	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("auto-migrate metadata column: %w", err)
	}
	return nil
}

func (s *MSSQLVectorStore) addContentColumn(ctx context.Context, table string) error {
	query := fmt.Sprintf("ALTER TABLE %s ADD %s NVARCHAR(MAX) NULL", qualifiedTable(s.opts.Schema, table), quoteIdent(contentColumn))
	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("auto-migrate content column: %w", err)
	}
	return nil
}

func (s *MSSQLVectorStore) readCollectionMetadata(ctx context.Context, collectionName string) (dimension int, metric vectordata.DistanceMetric, found bool, err error) {
	query := fmt.Sprintf("SELECT %s, %s FROM %s WHERE %s = @p1",
		quoteIdent("dimension"),
		quoteIdent("metric"),
		qualifiedTable(s.opts.Schema, collectionMetaTable),
		quoteIdent("name"),
	)

	var metricRaw string
	err = s.db.QueryRowContext(ctx, query, collectionName).Scan(&dimension, &metricRaw)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, "", false, nil
		}
		return 0, "", false, fmt.Errorf("read collection metadata: %w", err)
	}

	return dimension, vectordata.DistanceMetric(metricRaw), true, nil
}

func (s *MSSQLVectorStore) upsertCollectionMetadata(ctx context.Context, collectionName string, dimension int, metric vectordata.DistanceMetric) error {
	query := fmt.Sprintf(`
		MERGE %s AS target
		USING (
			SELECT @p1 AS %s, @p2 AS %s, @p3 AS %s
		) AS src
		ON target.%s = src.%s
		WHEN MATCHED THEN
			UPDATE SET
				target.%s = src.%s,
				target.%s = src.%s
		WHEN NOT MATCHED THEN
			INSERT (%s, %s, %s)
			VALUES (src.%s, src.%s, src.%s);
	`,
		qualifiedTable(s.opts.Schema, collectionMetaTable),
		quoteIdent("name"),
		quoteIdent("dimension"),
		quoteIdent("metric"),
		quoteIdent("name"),
		quoteIdent("name"),
		quoteIdent("dimension"),
		quoteIdent("dimension"),
		quoteIdent("metric"),
		quoteIdent("metric"),
		quoteIdent("name"),
		quoteIdent("dimension"),
		quoteIdent("metric"),
		quoteIdent("name"),
		quoteIdent("dimension"),
		quoteIdent("metric"),
	)

	if _, err := s.db.ExecContext(ctx, query, collectionName, dimension, string(metric)); err != nil {
		return fmt.Errorf("upsert collection metadata: %w", err)
	}
	return nil
}
