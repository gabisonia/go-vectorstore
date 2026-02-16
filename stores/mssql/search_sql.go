package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type searchSQLPlan struct {
	query      string
	args       []any
	projection vectordata.Projection
}

func (c *MSSQLCollection) buildSearchSQLPlan(vector []float32, topK int, opts vectordata.SearchOptions) (searchSQLPlan, error) {
	projection := resolveProjection(opts.Projection)
	vectorPayload, err := vectorJSON(vector)
	if err != nil {
		return searchSQLPlan{}, fmt.Errorf("encode query vector: %w", err)
	}

	distanceExpr, err := searchDistanceExpr(defaultMetric(c.metric))
	if err != nil {
		return searchSQLPlan{}, err
	}

	args := make([]any, 0, 8)
	nextArg := 1

	queryVectorPlaceholder := fmt.Sprintf("@p%d", nextArg)
	args = append(args, vectorPayload)
	nextArg++

	expectedDimPlaceholder := fmt.Sprintf("@p%d", nextArg)
	args = append(args, c.dimension)
	nextArg++

	whereParts := []string{
		fmt.Sprintf("candidate_stats.candidate_dim = %s", expectedDimPlaceholder),
		fmt.Sprintf("vec_stats.matched_dim = %s", expectedDimPlaceholder),
	}

	if opts.Filter != nil {
		filterSQL, filterArgs, next, err := compileMSSQLFilterSQL(opts.Filter, nextArg)
		if err != nil {
			return searchSQLPlan{}, err
		}
		if filterSQL != "" {
			whereParts = append(whereParts, filterSQL)
		}
		args = append(args, filterArgs...)
		nextArg = next
	}

	outerWhere := ""
	if opts.Threshold != nil {
		thresholdPlaceholder := fmt.Sprintf("@p%d", nextArg)
		args = append(args, *opts.Threshold)
		nextArg++
		outerWhere = fmt.Sprintf("WHERE ranked.%s <= %s", quoteIdent("distance"), thresholdPlaceholder)
	}

	limitPlaceholder := fmt.Sprintf("@p%d", nextArg)
	args = append(args, topK)

	innerSelectCols := []string{
		fmt.Sprintf("t.%s AS %s", quoteIdent(idColumn), quoteIdent(idColumn)),
	}
	outerSelectCols := []string{
		fmt.Sprintf("ranked.%s", quoteIdent(idColumn)),
	}
	if projection.IncludeVector {
		innerSelectCols = append(innerSelectCols, fmt.Sprintf("t.%s AS %s", quoteIdent(vectorColumn), quoteIdent(vectorColumn)))
		outerSelectCols = append(outerSelectCols, fmt.Sprintf("ranked.%s", quoteIdent(vectorColumn)))
	}
	if projection.IncludeMetadata {
		innerSelectCols = append(innerSelectCols, fmt.Sprintf("t.%s AS %s", quoteIdent(metadataColumn), quoteIdent(metadataColumn)))
		outerSelectCols = append(outerSelectCols, fmt.Sprintf("ranked.%s", quoteIdent(metadataColumn)))
	}
	if projection.IncludeContent {
		innerSelectCols = append(innerSelectCols, fmt.Sprintf("t.%s AS %s", quoteIdent(contentColumn), quoteIdent(contentColumn)))
		outerSelectCols = append(outerSelectCols, fmt.Sprintf("ranked.%s", quoteIdent(contentColumn)))
	}
	innerSelectCols = append(innerSelectCols, fmt.Sprintf("%s AS %s", distanceExpr, quoteIdent("distance")))
	outerSelectCols = append(outerSelectCols, fmt.Sprintf("ranked.%s", quoteIdent("distance")))

	query := fmt.Sprintf(`
SELECT %s
FROM (
	SELECT %s
	FROM %s AS t
	CROSS APPLY (
		SELECT
			COUNT(*) AS candidate_dim,
			SUM(CONVERT(float, tv.[value]) * CONVERT(float, tv.[value])) AS candidate_norm_sq
		FROM OPENJSON(t.%s) AS tv
	) AS candidate_stats
	CROSS APPLY (
		SELECT
			COUNT(*) AS matched_dim,
			SUM(POWER(CONVERT(float, tv.[value]) - CONVERT(float, qv.[value]), 2.0)) AS sum_sq_diff,
			SUM(CONVERT(float, tv.[value]) * CONVERT(float, qv.[value])) AS dot_product,
			SUM(CONVERT(float, qv.[value]) * CONVERT(float, qv.[value])) AS query_norm_sq
		FROM OPENJSON(t.%s) AS tv
		INNER JOIN OPENJSON(%s) AS qv ON qv.[key] = tv.[key]
	) AS vec_stats
	WHERE %s
) AS ranked
%s
ORDER BY ranked.%s ASC, ranked.%s ASC
OFFSET 0 ROWS FETCH NEXT %s ROWS ONLY`,
		strings.Join(outerSelectCols, ", "),
		strings.Join(innerSelectCols, ", "),
		c.tableName(),
		quoteIdent(vectorColumn),
		quoteIdent(vectorColumn),
		queryVectorPlaceholder,
		strings.Join(whereParts, " AND "),
		outerWhere,
		quoteIdent("distance"),
		quoteIdent(idColumn),
		limitPlaceholder,
	)

	return searchSQLPlan{
		query:      query,
		args:       args,
		projection: projection,
	}, nil
}

func (c *MSSQLCollection) executeSearchSQLPlan(ctx context.Context, plan searchSQLPlan) ([]vectordata.SearchResult, error) {
	rows, err := c.store.db.QueryContext(ctx, plan.query, plan.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]vectordata.SearchResult, 0)
	for rows.Next() {
		var rec vectordata.Record
		var vectorRaw string
		var metadataRaw string
		var content sql.NullString
		var distance float64

		scanTargets := []any{&rec.ID}
		if plan.projection.IncludeVector {
			scanTargets = append(scanTargets, &vectorRaw)
		}
		if plan.projection.IncludeMetadata {
			scanTargets = append(scanTargets, &metadataRaw)
		}
		if plan.projection.IncludeContent {
			scanTargets = append(scanTargets, &content)
		}
		scanTargets = append(scanTargets, &distance)

		if err := rows.Scan(scanTargets...); err != nil {
			return nil, err
		}

		if plan.projection.IncludeVector {
			parsedVector, err := parseVectorJSON(vectorRaw)
			if err != nil {
				return nil, fmt.Errorf("decode vector: %w", err)
			}
			rec.Vector = parsedVector
		}
		if plan.projection.IncludeMetadata {
			parsedMetadata, err := parseMetadataJSON(metadataRaw)
			if err != nil {
				return nil, fmt.Errorf("decode metadata: %w", err)
			}
			rec.Metadata = parsedMetadata
		}
		if plan.projection.IncludeContent && content.Valid {
			value := content.String
			rec.Content = &value
		}

		results = append(results, vectordata.SearchResult{
			Record:   rec,
			Distance: distance,
			Score:    vectordata.ScoreFromDistance(defaultMetric(c.metric), distance),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

func searchDistanceExpr(metric vectordata.DistanceMetric) (string, error) {
	switch metric {
	case vectordata.DistanceCosine:
		return `CASE
			WHEN vec_stats.query_norm_sq = 0 OR candidate_stats.candidate_norm_sq = 0 THEN 1.0
			ELSE 1.0 - (vec_stats.dot_product / (SQRT(vec_stats.query_norm_sq) * SQRT(candidate_stats.candidate_norm_sq)))
		END`, nil
	case vectordata.DistanceL2:
		return "SQRT(vec_stats.sum_sq_diff)", nil
	case vectordata.DistanceInnerProduct:
		return "(-vec_stats.dot_product)", nil
	default:
		return "", fmt.Errorf("%w: unsupported distance metric %q", vectordata.ErrSchemaMismatch, metric)
	}
}
