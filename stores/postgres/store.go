package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StoreOptions configures PostgresVectorStore behavior.
type StoreOptions struct {
	Schema          string
	EnsureExtension bool
	StrictByDefault bool
}

// DefaultStoreOptions returns production-safe defaults.
func DefaultStoreOptions() StoreOptions {
	return StoreOptions{
		Schema:          "public",
		EnsureExtension: true,
		StrictByDefault: true,
	}
}

// PostgresVectorStore implements vectordata.VectorStore using pgxpool.
type PostgresVectorStore struct {
	pool *pgxpool.Pool
	opts StoreOptions
}

// NewVectorStore creates a Postgres-backed vector store.
func NewVectorStore(pool *pgxpool.Pool, opts StoreOptions) (*PostgresVectorStore, error) {
	if pool == nil {
		return nil, fmt.Errorf("nil pgx pool")
	}
	normalized := opts.withDefaults()
	if err := normalized.validate(); err != nil {
		return nil, err
	}
	return &PostgresVectorStore{pool: pool, opts: normalized}, nil
}

// Collection returns a handle to a collection without schema checks.
func (s *PostgresVectorStore) Collection(name string, dimension int, metric vectordata.DistanceMetric) vectordata.Collection {
	return s.newCollectionHandle(name, dimension, metric)
}

// EnsureCollection creates or validates a collection schema and returns its handle.
func (s *PostgresVectorStore) EnsureCollection(ctx context.Context, spec vectordata.CollectionSpec) (vectordata.Collection, error) {
	normalizedSpec, mode, err := s.normalizeCollectionSpec(spec)
	if err != nil {
		return nil, err
	}

	if err := s.ensureBaseSchema(ctx); err != nil {
		return nil, err
	}

	if err := s.ensureTableWithValidation(ctx, normalizedSpec.Name, normalizedSpec.Dimension, mode); err != nil {
		return nil, err
	}

	return s.newCollectionHandle(normalizedSpec.Name, normalizedSpec.Dimension, normalizedSpec.Metric), nil
}

func (s *PostgresVectorStore) normalizeCollectionSpec(spec vectordata.CollectionSpec) (vectordata.CollectionSpec, vectordata.EnsureMode, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return vectordata.CollectionSpec{}, "", fmt.Errorf("%w: collection name is empty", vectordata.ErrSchemaMismatch)
	}
	if spec.Dimension <= 0 {
		return vectordata.CollectionSpec{}, "", fmt.Errorf("%w: dimension must be > 0", vectordata.ErrSchemaMismatch)
	}
	spec.Metric = defaultMetric(spec.Metric)
	if _, err := metricOperator(spec.Metric); err != nil {
		return vectordata.CollectionSpec{}, "", err
	}

	mode := defaultMode(spec.Mode, s.opts.StrictByDefault)
	if mode != vectordata.EnsureStrict && mode != vectordata.EnsureAutoMigrate {
		return vectordata.CollectionSpec{}, "", fmt.Errorf("%w: unsupported ensure mode %q", vectordata.ErrSchemaMismatch, mode)
	}
	return spec, mode, nil
}

func (s *PostgresVectorStore) ensureTableWithValidation(ctx context.Context, tableName string, dimension int, mode vectordata.EnsureMode) error {
	exists, err := s.tableExists(ctx, tableName)
	if err != nil {
		return err
	}
	if !exists {
		if err := s.createCollectionTable(ctx, tableName, dimension); err != nil {
			return err
		}
		return nil
	}
	return s.validateCollectionSchema(ctx, tableName, dimension, mode)
}

func (s *PostgresVectorStore) newCollectionHandle(name string, dimension int, metric vectordata.DistanceMetric) vectordata.Collection {
	return &PostgresCollection{
		store:     s,
		name:      name,
		dimension: dimension,
		metric:    defaultMetric(metric),
	}
}

func (o StoreOptions) withDefaults() StoreOptions {
	if strings.TrimSpace(o.Schema) == "" {
		o.Schema = "public"
	}
	return o
}

func (o StoreOptions) validate() error {
	if strings.TrimSpace(o.Schema) == "" {
		return fmt.Errorf("%w: schema is empty", vectordata.ErrSchemaMismatch)
	}
	return nil
}
