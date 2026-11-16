package postgres

import (
	"context"
	"iter"

	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ vectordata.BinarySearcher = (*ModelCollection)(nil)
var _ vectordata.BinaryHybridSearcher = (*ModelCollection)(nil)

func binaryWriteValue(v vectordata.BinaryVector) pgtype.Bits {
	return pgtype.Bits{Bytes: v.Bytes, Len: int32(v.Dimensions), Valid: true}
}

func modelMetricOperator(p vectordata.VectorProperty) (string, error) {
	if p.Kind == vectordata.VectorBinary {
		return "<~>", nil
	}
	return metricOperator(p.Metric)
}

func binaryMigrationPrerequisites(ctx context.Context, tx pgx.Tx, d vectordata.CollectionDefinition) ([]vectordata.MigrationStep, error) {
	needed := false
	methods := map[vectordata.IndexMethod]bool{}
	for _, p := range d.Vectors {
		if p.Kind == vectordata.VectorBinary {
			needed = true
			if p.Index != nil {
				methods[p.Index.Method] = true
			}
		}
	}
	if !needed {
		return nil, nil
	}
	var present bool
	if err := tx.QueryRow(ctx, `SELECT to_regoperator('<~>(bit,bit)') IS NOT NULL`).Scan(&present); err != nil {
		return nil, err
	}
	var steps []vectordata.MigrationStep
	if !present {
		steps = append(steps, vectordata.MigrationStep{Kind: vectordata.MigrationPrerequisite, Manual: true, Description: "binary Hamming search requires pgvector 0.7+ and a visible <~>(bit,bit) operator"})
	}
	for _, method := range []vectordata.IndexMethod{vectordata.IndexMethodHNSW, vectordata.IndexMethodIVFFlat} {
		if !methods[method] {
			continue
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_opclass oc JOIN pg_am am ON am.oid=oc.opcmethod WHERE oc.opcname='bit_hamming_ops' AND oc.opcintype='bit'::regtype AND am.amname=$1 AND pg_opclass_is_visible(oc.oid))`, string(method)).Scan(&present); err != nil {
			return nil, err
		}
		if !present {
			steps = append(steps, vectordata.MigrationStep{Kind: vectordata.MigrationPrerequisite, Manual: true, Description: "binary Hamming indexes require a visible bit_hamming_ops class for " + string(method)})
		}
	}
	return steps, nil
}

func (c *ModelCollection) binarySearchPlan(v vectordata.BinaryVector, top int, opts vectordata.ModelSearchOptions) (string, []any, vectordata.VectorProperty, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return "", nil, p, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return "", nil, p, err
	}
	v, err = vectordata.NormalizeBinaryVector(v, p)
	if err != nil {
		return "", nil, p, err
	}
	return c.searchValuePlan(binaryWriteValue(v), top, opts)
}
func (c *ModelCollection) SearchBinaryIter(ctx context.Context, v vectordata.BinaryVector, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return func(yield func(vectordata.ModelSearchResult, error) bool) {
		sql, args, p, err := c.binarySearchPlan(v, top, opts)
		if err != nil {
			yield(vectordata.ModelSearchResult{}, c.operation("search_binary", err))
			return
		}
		c.runSearch(ctx, sql, args, p, opts, false, yield)
	}
}
func (c *ModelCollection) SearchBinary(ctx context.Context, v vectordata.BinaryVector, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	out := []vectordata.ModelSearchResult{}
	for r, err := range c.SearchBinaryIter(ctx, v, top, opts) {
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func (c *ModelCollection) HybridSearchBinary(ctx context.Context, v vectordata.BinaryVector, keywords string, top int, opts vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return iteration.Collect(c.HybridSearchBinaryIter(ctx, v, keywords, top, opts))
}
