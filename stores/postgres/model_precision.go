package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

func vectorSQLType(p vectordata.VectorProperty) string {
	if p.Kind == vectordata.VectorBinary {
		return "bit"
	}
	if p.Precision == vectordata.VectorFloat16 {
		return "halfvec"
	}
	return "vector"
}

func vectorColumnType(p vectordata.VectorProperty) string {
	return fmt.Sprintf("%s(%d)", vectorSQLType(p), p.Dimensions)
}

func vectorQuerySQLType(p vectordata.VectorProperty) string {
	// An unqualified ::bit cast silently truncates the literal to one bit.
	if p.Kind == vectordata.VectorBinary {
		return vectorColumnType(p)
	}
	return vectorSQLType(p)
}

func vectorOpClass(p vectordata.VectorProperty) (string, error) {
	if p.Kind == vectordata.VectorBinary {
		return "bit_hamming_ops", nil
	}
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
		if p.Kind != vectordata.VectorBinary && p.Dimensions > 16000 {
			return fmt.Errorf("%w: PostgreSQL vector storage supports at most 16000 dimensions", vectordata.ErrSchemaMismatch)
		}
		if p.Index != nil {
			limit, err := modelVectorIndexLimit(p)
			if err != nil {
				return err
			}
			if p.Dimensions > limit {
				return fmt.Errorf("%w: %s indexes support at most %d dimensions", vectordata.ErrSchemaMismatch, vectorSQLType(p), limit)
			}
		}
	}
	return nil
}

// These rules drive both local schema validation and capability discovery.
func vectorIndexLimit(precision vectordata.VectorPrecision, metric vectordata.DistanceMetric, method vectordata.IndexMethod) (int, error) {
	if err := metric.Validate(); err != nil {
		return 0, err
	}
	if metric == vectordata.DistanceHamming {
		return 0, fmt.Errorf("%w: Hamming requires binary storage", vectordata.ErrSchemaMismatch)
	}
	if method != vectordata.IndexMethodHNSW && method != vectordata.IndexMethodIVFFlat {
		return 0, fmt.Errorf("%w: unsupported vector index method %q", vectordata.ErrSchemaMismatch, method)
	}
	if metric == vectordata.DistanceL1 && method == vectordata.IndexMethodIVFFlat {
		return 0, fmt.Errorf("%w: L1 distance supports HNSW indexes, not IVFFlat", vectordata.ErrSchemaMismatch)
	}
	switch precision {
	case "", vectordata.VectorFloat32:
		return 2000, nil
	case vectordata.VectorFloat16:
		return 4000, nil
	default:
		return 0, fmt.Errorf("%w: unsupported vector precision %q", vectordata.ErrSchemaMismatch, precision)
	}
}

func modelVectorIndexLimit(p vectordata.VectorProperty) (int, error) {
	if p.Kind == vectordata.VectorBinary {
		if p.Metric != vectordata.DistanceHamming || p.Precision != "" || p.Index == nil || p.Index.Method != vectordata.IndexMethodHNSW && p.Index.Method != vectordata.IndexMethodIVFFlat {
			return 0, fmt.Errorf("%w: unsupported binary index", vectordata.ErrSchemaMismatch)
		}
		return 64000, nil
	}
	return vectorIndexLimit(p.Precision, p.Metric, p.Index.Method)
}

func (c *ModelCollection) validateVectorTypes(ctx context.Context, tx pgx.Tx) error {
	if usesHalfVectors(c.definition) {
		var available bool
		if err := tx.QueryRow(ctx, "SELECT to_regtype('halfvec') IS NOT NULL").Scan(&available); err != nil {
			return err
		}
		if !available {
			return fmt.Errorf("%w: float16 storage requires pgvector 0.7+ and a visible halfvec type", vectordata.ErrUnsupported)
		}
	}
	steps, err := l1MigrationPrerequisites(ctx, tx, c.definition)
	if err != nil {
		return err
	}
	binary, err := binaryMigrationPrerequisites(ctx, tx, c.definition)
	if err != nil {
		return err
	}
	steps = append(steps, binary...)
	if len(steps) != 0 {
		return fmt.Errorf("%w: %s", vectordata.ErrUnsupported, steps[0].Description)
	}
	return nil
}

// Detect availability without relying on extension version strings. Iteration is
// stable because these steps are also included in serialized migration reviews.
func l1MigrationPrerequisites(ctx context.Context, tx pgx.Tx, d vectordata.CollectionDefinition) ([]vectordata.MigrationStep, error) {
	needed := map[string]bool{}
	for _, p := range d.Vectors {
		if p.Metric == vectordata.DistanceL1 {
			needed[vectorSQLType(p)] = needed[vectorSQLType(p)] || p.Index != nil
		}
	}
	var steps []vectordata.MigrationStep
	for _, typ := range []string{"vector", "halfvec"} {
		index, present := needed[typ]
		if !present {
			continue
		}
		var operator, opClass bool
		err := tx.QueryRow(ctx, `SELECT CASE WHEN to_regtype($1) IS NULL THEN false ELSE to_regoperator($2) IS NOT NULL END,
 EXISTS (SELECT 1 FROM pg_opclass oc JOIN pg_am am ON am.oid=oc.opcmethod
 WHERE oc.opcname=$3 AND oc.opcintype=to_regtype($1) AND am.amname='hnsw' AND pg_opclass_is_visible(oc.oid))`, typ, "<+>("+typ+","+typ+")", typ+"_l1_ops").Scan(&operator, &opClass)
		if err != nil {
			return nil, err
		}
		if !operator {
			steps = append(steps, vectordata.MigrationStep{Kind: vectordata.MigrationPrerequisite, Manual: true, Description: "L1 search requires pgvector 0.7+ and a visible <+> operator for " + typ})
		}
		if index && !opClass {
			steps = append(steps, vectordata.MigrationStep{Kind: vectordata.MigrationPrerequisite, Manual: true, Description: "L1 HNSW indexes require pgvector 0.7+ and a visible " + typ + "_l1_ops operator class"})
		}
	}
	return steps, nil
}
