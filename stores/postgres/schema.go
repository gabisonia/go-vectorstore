package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func (s *PostgresVectorStore) ensureBaseSchema(ctx context.Context) error {
	if s.opts.EnsureExtension {
		if _, err := s.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
			return fmt.Errorf("ensure pgvector extension: %w", err)
		}
	}

	query := fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, quoteIdent(s.opts.Schema))
	if _, err := s.pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("ensure schema %q: %w", s.opts.Schema, err)
	}
	return nil
}

func (s *PostgresVectorStore) tableExists(ctx context.Context, table string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = $1 AND table_name = $2
		)`,
		s.opts.Schema,
		table,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check table exists: %w", err)
	}
	return exists, nil
}

func (s *PostgresVectorStore) createCollectionTable(ctx context.Context, table string, dimension int) error {
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			%s text PRIMARY KEY,
			%s vector(%d) NOT NULL,
			%s jsonb NOT NULL DEFAULT '{}'::jsonb,
			%s text
		)
	`,
		qualifiedTable(s.opts.Schema, table),
		quoteIdent(idColumn),
		quoteIdent(vectorColumn),
		dimension,
		quoteIdent(metadataColumn),
		quoteIdent(contentColumn),
	)
	if _, err := s.pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("create collection table %q: %w", table, err)
	}
	return nil
}

func (s *PostgresVectorStore) validateCollectionSchema(ctx context.Context, table string, expectedDimension int, mode vectordata.EnsureMode) error {
	type columnInfo struct {
		dataType string
		udtName  string
	}

	rows, err := s.pool.Query(ctx,
		`SELECT column_name, data_type, udt_name
		 FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2`,
		s.opts.Schema,
		table,
	)
	if err != nil {
		return fmt.Errorf("read schema columns: %w", err)
	}
	defer rows.Close()

	cols := map[string]columnInfo{}
	for rows.Next() {
		var name string
		var info columnInfo
		if err := rows.Scan(&name, &info.dataType, &info.udtName); err != nil {
			return fmt.Errorf("scan schema columns: %w", err)
		}
		cols[name] = info
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema columns: %w", err)
	}

	if _, ok := cols[idColumn]; !ok {
		return fmt.Errorf("%w: missing column %q", vectordata.ErrSchemaMismatch, idColumn)
	}
	if _, ok := cols[vectorColumn]; !ok {
		return fmt.Errorf("%w: missing column %q", vectordata.ErrSchemaMismatch, vectorColumn)
	}

	if cols[idColumn].dataType != "text" {
		return fmt.Errorf("%w: expected %q data type text, got %q", vectordata.ErrSchemaMismatch, idColumn, cols[idColumn].dataType)
	}
	if cols[vectorColumn].udtName != "vector" {
		return fmt.Errorf("%w: expected %q type vector, got %q", vectordata.ErrSchemaMismatch, vectorColumn, cols[vectorColumn].udtName)
	}

	if err := s.ensurePrimaryKeyOnID(ctx, table); err != nil {
		return err
	}

	if _, ok := cols[metadataColumn]; !ok {
		if mode == vectordata.EnsureStrict {
			return fmt.Errorf("%w: missing column %q", vectordata.ErrSchemaMismatch, metadataColumn)
		}
		if err := s.addMetadataColumn(ctx, table); err != nil {
			return err
		}
	} else if cols[metadataColumn].udtName != "jsonb" {
		return fmt.Errorf("%w: expected %q type jsonb, got %q", vectordata.ErrSchemaMismatch, metadataColumn, cols[metadataColumn].udtName)
	}

	if _, ok := cols[contentColumn]; !ok {
		if mode == vectordata.EnsureStrict {
			return fmt.Errorf("%w: missing column %q", vectordata.ErrSchemaMismatch, contentColumn)
		}
		if err := s.addContentColumn(ctx, table); err != nil {
			return err
		}
	} else if cols[contentColumn].dataType != "text" {
		return fmt.Errorf("%w: expected %q data type text, got %q", vectordata.ErrSchemaMismatch, contentColumn, cols[contentColumn].dataType)
	}

	dimension, err := s.readVectorDimension(ctx, table)
	if err != nil {
		return err
	}
	if dimension != expectedDimension {
		return fmt.Errorf("%w: expected vector dimension %d, got %d", vectordata.ErrSchemaMismatch, expectedDimension, dimension)
	}

	return nil
}

func (s *PostgresVectorStore) ensurePrimaryKeyOnID(ctx context.Context, table string) error {
	var hasPK bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu
				ON tc.constraint_name = kcu.constraint_name
				AND tc.table_schema = kcu.table_schema
				AND tc.table_name = kcu.table_name
			WHERE tc.table_schema = $1
				AND tc.table_name = $2
				AND tc.constraint_type = 'PRIMARY KEY'
				AND kcu.column_name = $3
		)
	`, s.opts.Schema, table, idColumn).Scan(&hasPK)
	if err != nil {
		return fmt.Errorf("check primary key: %w", err)
	}
	if !hasPK {
		return fmt.Errorf("%w: primary key on %q is required", vectordata.ErrSchemaMismatch, idColumn)
	}
	return nil
}

func (s *PostgresVectorStore) addMetadataColumn(ctx context.Context, table string) error {
	query := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s jsonb NOT NULL DEFAULT '{}'::jsonb`,
		qualifiedTable(s.opts.Schema, table),
		quoteIdent(metadataColumn),
	)
	if _, err := s.pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("auto-migrate metadata column: %w", err)
	}
	return nil
}

func (s *PostgresVectorStore) addContentColumn(ctx context.Context, table string) error {
	query := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s text`,
		qualifiedTable(s.opts.Schema, table),
		quoteIdent(contentColumn),
	)
	if _, err := s.pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("auto-migrate content column: %w", err)
	}
	return nil
}

func (s *PostgresVectorStore) readVectorDimension(ctx context.Context, table string) (int, error) {
	var typeName string
	err := s.pool.QueryRow(ctx, `
		SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relname = $2
		  AND a.attname = $3
		  AND a.attnum > 0
		  AND NOT a.attisdropped
	`, s.opts.Schema, table, vectorColumn).Scan(&typeName)
	if err != nil {
		return 0, fmt.Errorf("read vector type: %w", err)
	}
	dim, err := parseVectorDimension(typeName)
	if err != nil {
		return 0, fmt.Errorf("parse vector dimension from %q: %w", typeName, err)
	}
	return dim, nil
}

func parseVectorDimension(typeName string) (int, error) {
	typeName = strings.TrimSpace(typeName)
	if !strings.HasPrefix(typeName, "vector(") || !strings.HasSuffix(typeName, ")") {
		return 0, fmt.Errorf("unexpected vector type %q", typeName)
	}
	inside := strings.TrimSuffix(strings.TrimPrefix(typeName, "vector("), ")")
	dim, err := strconv.Atoi(inside)
	if err != nil {
		return 0, fmt.Errorf("invalid vector dimension %q", inside)
	}
	if dim <= 0 {
		return 0, fmt.Errorf("invalid vector dimension %d", dim)
	}
	return dim, nil
}
