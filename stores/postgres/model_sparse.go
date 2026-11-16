package postgres

import (
	"context"
	"fmt"
	"iter"
	"strconv"
	"strings"

	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

var _ vectordata.SparseSearcher = (*ModelCollection)(nil)
var _ vectordata.SparseHybridSearcher = (*ModelCollection)(nil)

// PostgreSQL text indices are one-based; Go coordinates stay zero-based.
func sparseLiteral(v vectordata.SparseVector) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, index := range v.Indices {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(index + 1))
		b.WriteByte(':')
		b.WriteString(strconv.FormatFloat(float64(v.Values[i]), 'g', -1, 32))
	}
	b.WriteString("}/")
	b.WriteString(strconv.Itoa(v.Dimensions))
	return b.String()
}

func parseSparseText(text string, p vectordata.VectorProperty) (vectordata.SparseVector, error) {
	invalid := func() (vectordata.SparseVector, error) {
		return vectordata.SparseVector{}, fmt.Errorf("%w: invalid stored sparse vector %q", vectordata.ErrInvalidVector, p.Name)
	}
	if !strings.HasPrefix(text, "{") {
		return invalid()
	}
	body, dimensions, ok := strings.Cut(text[1:], "}/")
	if !ok {
		return invalid()
	}
	dim, err := strconv.Atoi(dimensions)
	if err != nil {
		return invalid()
	}
	v := vectordata.SparseVector{Dimensions: dim}
	if body != "" {
		if strings.Count(body, ",")+1 > vectordata.MaxSparseNonZero {
			return invalid()
		}
		for _, entry := range strings.Split(body, ",") {
			index, value, ok := strings.Cut(entry, ":")
			if !ok {
				return invalid()
			}
			n, err := strconv.ParseInt(index, 10, 32)
			if err != nil || n < 1 {
				return invalid()
			}
			x, err := strconv.ParseFloat(value, 32)
			if err != nil {
				return invalid()
			}
			v.Indices = append(v.Indices, int(n)-1)
			v.Values = append(v.Values, float32(x))
		}
	}
	return vectordata.NormalizeSparseVector(v, p)
}

func sparseMigrationPrerequisites(ctx context.Context, tx pgx.Tx, d vectordata.CollectionDefinition) ([]vectordata.MigrationStep, error) {
	needed := map[vectordata.DistanceMetric]bool{}
	for _, p := range d.Vectors {
		if p.Kind == vectordata.VectorSparse {
			needed[p.Metric] = needed[p.Metric] || p.Index != nil
		}
	}
	if len(needed) == 0 {
		return nil, nil
	}
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT to_regtype('sparsevec') IS NOT NULL`).Scan(&visible); err != nil {
		return nil, err
	}
	if !visible {
		return []vectordata.MigrationStep{{Kind: vectordata.MigrationPrerequisite, Manual: true, Description: "sparse vectors require pgvector 0.7+ and a visible sparsevec type"}}, nil
	}
	var steps []vectordata.MigrationStep
	for _, metric := range []vectordata.DistanceMetric{vectordata.DistanceCosine, vectordata.DistanceL2, vectordata.DistanceInnerProduct, vectordata.DistanceL1} {
		indexed, present := needed[metric]
		if !present {
			continue
		}
		op, _ := metricOperator(metric)
		class, _ := vectorOpClass(vectordata.VectorProperty{Kind: vectordata.VectorSparse, Metric: metric})
		var operator, opclass bool
		if err := tx.QueryRow(ctx, `SELECT to_regoperator($1) IS NOT NULL,EXISTS(SELECT 1 FROM pg_opclass oc JOIN pg_am am ON am.oid=oc.opcmethod WHERE oc.opcname=$2 AND oc.opcintype=to_regtype('sparsevec') AND am.amname='hnsw' AND pg_opclass_is_visible(oc.oid))`, op+"(sparsevec,sparsevec)", class).Scan(&operator, &opclass); err != nil {
			return nil, err
		}
		if !operator {
			steps = append(steps, vectordata.MigrationStep{Kind: vectordata.MigrationPrerequisite, Manual: true, Description: "sparse search requires a visible " + op + "(sparsevec,sparsevec) operator"})
		}
		if indexed && !opclass {
			steps = append(steps, vectordata.MigrationStep{Kind: vectordata.MigrationPrerequisite, Manual: true, Description: "sparse HNSW indexes require a visible " + class + " operator class"})
		}
	}
	return steps, nil
}

func (c *ModelCollection) sparseIndexBackfillSteps(ctx context.Context, tx pgx.Tx, registered vectordata.CollectionDefinition) ([]vectordata.MigrationStep, error) {
	var steps []vectordata.MigrationStep
	for _, p := range c.definition.Vectors {
		if p.Kind != vectordata.VectorSparse || p.Index == nil {
			continue
		}
		for _, old := range registered.Vectors {
			if old.Name != p.Name || old.StorageName != p.StorageName || old.Kind != vectordata.VectorSparse || old.Index != nil {
				continue
			}
			// sparsevec_send is a 12-byte header plus 8 bytes per nonzero pair.
			// Checking its size avoids expanding potentially billion-dimensional values.
			var tooMany bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+c.table()+" WHERE octet_length(sparsevec_send("+quoteIdent(p.StorageName)+"))>$1)", 12+8*vectordata.MaxIndexedSparseNonZero).Scan(&tooMany); err != nil {
				return nil, err
			}
			if tooMany {
				steps = append(steps, vectordata.MigrationStep{Kind: vectordata.MigrationBackfill, Property: p.Name, StorageName: p.StorageName, Manual: true, RequiresBackfill: true, RequiresReembedding: true, Description: "reduce or regenerate sparse vectors above the 1000-nonzero HNSW limit before adding the index"})
			}
		}
	}
	return steps, nil
}

func (c *ModelCollection) sparseSearchPlan(v vectordata.SparseVector, top int, opts vectordata.ModelSearchOptions) (string, []any, vectordata.VectorProperty, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return "", nil, p, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return "", nil, p, err
	}
	v, err = vectordata.NormalizeSparseVector(v, p)
	if err != nil {
		return "", nil, p, err
	}
	return c.searchValuePlan(sparseLiteral(v), top, opts)
}
func (c *ModelCollection) SearchSparseIter(ctx context.Context, v vectordata.SparseVector, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return func(yield func(vectordata.ModelSearchResult, error) bool) {
		sql, args, p, err := c.sparseSearchPlan(v, top, opts)
		if err != nil {
			yield(vectordata.ModelSearchResult{}, c.operation("search_sparse", err))
			return
		}
		c.runSearch(ctx, sql, args, p, opts, false, yield)
	}
}
func (c *ModelCollection) SearchSparse(ctx context.Context, v vectordata.SparseVector, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	out := []vectordata.ModelSearchResult{}
	for r, err := range c.SearchSparseIter(ctx, v, top, opts) {
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func (c *ModelCollection) HybridSearchSparse(ctx context.Context, v vectordata.SparseVector, keywords string, top int, opts vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return iteration.Collect(c.HybridSearchSparseIter(ctx, v, keywords, top, opts))
}

func modelVectorTuning(value any, p vectordata.VectorProperty) ([]searchSetting, bool, bool, error) {
	if p.Kind == vectordata.VectorSparse {
		var tuning SearchTuning
		switch v := value.(type) {
		case SearchTuning:
			tuning = v
		case *SearchTuning:
			if v != nil {
				tuning = *v
			}
		}
		if tuning.IVFFlat != nil {
			return nil, false, false, fmt.Errorf("%w: sparse vectors do not support IVFFlat tuning", vectordata.ErrInvalidSearchOptions)
		}
	}
	return modelTuning(value, p.Metric)
}
