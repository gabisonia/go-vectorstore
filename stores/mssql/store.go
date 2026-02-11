package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// StoreOptions configures MSSQLVectorStore behavior.
type StoreOptions struct {
	Schema          string
	StrictByDefault bool
}

// DefaultStoreOptions returns production-safe defaults.
func DefaultStoreOptions() StoreOptions {
	return StoreOptions{
		Schema:          "dbo",
		StrictByDefault: true,
	}
}

// MSSQLVectorStore implements vectordata.VectorStore using database/sql.
type MSSQLVectorStore struct {
	db   *sql.DB
	opts StoreOptions
}

// NewVectorStore creates a SQL Server-backed vector store.
func NewVectorStore(db *sql.DB, opts StoreOptions) (*MSSQLVectorStore, error) {
	if db == nil {
		return nil, fmt.Errorf("nil sql db")
	}

	normalized := opts.withDefaults()
	if err := normalized.validate(); err != nil {
		return nil, err
	}

	return &MSSQLVectorStore{db: db, opts: normalized}, nil
}

// Collection returns a handle to a collection without schema checks.
func (s *MSSQLVectorStore) Collection(name string, dimension int, metric vectordata.DistanceMetric) vectordata.Collection {
	return s.newCollectionHandle(name, dimension, metric)
}

// EnsureCollection creates or validates a collection schema and returns its handle.
func (s *MSSQLVectorStore) EnsureCollection(ctx context.Context, spec vectordata.CollectionSpec) (vectordata.Collection, error) {
	normalizedSpec, mode, err := s.normalizeCollectionSpec(spec)
	if err != nil {
		return nil, err
	}

	if err := s.ensureBaseSchema(ctx); err != nil {
		return nil, err
	}
	if err := s.ensureTableWithValidation(ctx, normalizedSpec, mode); err != nil {
		return nil, err
	}

	return s.newCollectionHandle(normalizedSpec.Name, normalizedSpec.Dimension, normalizedSpec.Metric), nil
}

func (s *MSSQLVectorStore) normalizeCollectionSpec(spec vectordata.CollectionSpec) (vectordata.CollectionSpec, vectordata.EnsureMode, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return vectordata.CollectionSpec{}, "", fmt.Errorf("%w: collection name is empty", vectordata.ErrSchemaMismatch)
	}
	if spec.Dimension <= 0 {
		return vectordata.CollectionSpec{}, "", fmt.Errorf("%w: dimension must be > 0", vectordata.ErrSchemaMismatch)
	}

	spec.Metric = defaultMetric(spec.Metric)
	if err := spec.Metric.Validate(); err != nil {
		return vectordata.CollectionSpec{}, "", err
	}

	mode := defaultMode(spec.Mode, s.opts.StrictByDefault)
	if mode != vectordata.EnsureStrict && mode != vectordata.EnsureAutoMigrate {
		return vectordata.CollectionSpec{}, "", fmt.Errorf("%w: unsupported ensure mode %q", vectordata.ErrSchemaMismatch, mode)
	}

	return spec, mode, nil
}

func (s *MSSQLVectorStore) newCollectionHandle(name string, dimension int, metric vectordata.DistanceMetric) vectordata.Collection {
	return &MSSQLCollection{
		store:     s,
		name:      strings.TrimSpace(name),
		dimension: dimension,
		metric:    defaultMetric(metric),
	}
}

func (s StoreOptions) withDefaults() StoreOptions {
	if strings.TrimSpace(s.Schema) == "" {
		s.Schema = "dbo"
	}
	return s
}

func (s StoreOptions) validate() error {
	if strings.TrimSpace(s.Schema) == "" {
		return fmt.Errorf("%w: schema is empty", vectordata.ErrSchemaMismatch)
	}
	return nil
}
