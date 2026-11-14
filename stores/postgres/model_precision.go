package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

func vectorSQLType(p vectordata.VectorProperty) string {
	if p.Precision == vectordata.VectorFloat16 {
		return "halfvec"
	}
	return "vector"
}

func vectorColumnType(p vectordata.VectorProperty) string {
	return fmt.Sprintf("%s(%d)", vectorSQLType(p), p.Dimensions)
}

func vectorOpClass(p vectordata.VectorProperty) (string, error) {
	op, err := metricOpClass(p.Metric)
	if p.Precision == vectordata.VectorFloat16 {
		op = "halfvec_" + strings.TrimPrefix(op, "vector_")
	}
	return op, err
}

func usesHalfVectors(d vectordata.CollectionDefinition) bool {
	for _, p := range d.Vectors {
		if p.Precision == vectordata.VectorFloat16 {
			return true
		}
	}
	return false
}

func validateVectorDimensions(d vectordata.CollectionDefinition) error {
	for _, p := range d.Vectors {
		if p.Dimensions > 16000 {
			return fmt.Errorf("%w: PostgreSQL vector storage supports at most 16000 dimensions", vectordata.ErrSchemaMismatch)
		}
		limit := 2000
		if p.Precision == vectordata.VectorFloat16 {
			limit = 4000
		}
		if p.Index != nil && p.Dimensions > limit {
			return fmt.Errorf("%w: %s indexes support at most %d dimensions", vectordata.ErrSchemaMismatch, vectorSQLType(p), limit)
		}
	}
	return nil
}

func (c *ModelCollection) validateVectorTypes(ctx context.Context, tx pgx.Tx) error {
	if !usesHalfVectors(c.definition) {
		return nil
	}
	var available bool
	if err := tx.QueryRow(ctx, "SELECT to_regtype('halfvec') IS NOT NULL").Scan(&available); err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("%w: float16 storage requires pgvector 0.7+ and a visible halfvec type", vectordata.ErrUnsupported)
	}
	return nil
}
