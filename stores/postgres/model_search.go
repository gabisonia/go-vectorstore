package postgres

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/stores/postgres/filtersql"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

// SearchTuning contains pgvector-specific settings for the model API.
// Set ModelSearchOptions.ProviderOptions to this value (or a pointer to it).
type SearchTuning struct {
	HNSW    *vectordata.HNSWSearchOptions
	IVFFlat *vectordata.IVFFlatSearchOptions
}

func modelTuning(value any, metric vectordata.DistanceMetric) ([]searchSetting, bool, bool, error) {
	var t SearchTuning
	switch v := value.(type) {
	case nil:
	case SearchTuning:
		t = v
	case *SearchTuning:
		if v != nil {
			t = *v
		}
	default:
		return nil, false, false, fmt.Errorf("%w: unknown PostgreSQL search tuning %T", vectordata.ErrUnsupported, value)
	}
	if metric == vectordata.DistanceL1 && t.IVFFlat != nil {
		return nil, false, false, fmt.Errorf("%w: IVFFlat tuning is unavailable for L1 distance", vectordata.ErrInvalidSearchOptions)
	}
	settings, iterative, err := searchSettings(vectordata.SearchOptions{HNSW: t.HNSW, IVFFlat: t.IVFFlat})
	return settings, iterative, t.IVFFlat != nil && t.IVFFlat.IterativeScan, err
}
func scoreSQL(distance string, metric vectordata.DistanceMetric) string {
	switch metric {
	case vectordata.DistanceCosine:
		return "(1 - " + distance + ")"
	case vectordata.DistanceL2, vectordata.DistanceL1, vectordata.DistanceHamming:
		return "(1 / (1 + " + distance + "))"
	default:
		return "(-" + distance + ")"
	}
}
func (c *ModelCollection) searchPlan(vector []float32, top int, opts vectordata.ModelSearchOptions) (string, []any, vectordata.VectorProperty, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return "", nil, p, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return "", nil, p, err
	}
	vector, err = vectordata.NormalizeModelVector(vector, p)
	if err != nil {
		return "", nil, p, err
	}
	return c.searchValuePlan(vectorLiteral(vector), top, opts)
}
func (c *ModelCollection) searchValuePlan(literal any, top int, opts vectordata.ModelSearchOptions) (string, []any, vectordata.VectorProperty, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return "", nil, p, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return "", nil, p, err
	}
	_, _, relaxed, err := modelVectorTuning(opts.ProviderOptions, p)
	if err != nil {
		return "", nil, p, err
	}
	op, err := modelMetricOperator(p)
	if err != nil {
		return "", nil, p, err
	}
	distance := "(t." + quoteIdent(p.StorageName) + " " + op + " $1::" + vectorQuerySQLType(p) + ")"
	if err := vectordata.ValidateModelFilter(opts.Filter, c.definition); err != nil {
		return "", nil, p, err
	}
	where, args, next, err := filtersql.CompileFilterSQL(opts.Filter, c.filterConfig(), 2)
	if err != nil {
		return "", nil, p, err
	}
	args = append([]any{literal}, args...)
	predicates := []string{"t." + quoteIdent(p.StorageName) + " IS NOT NULL"}
	if where != "" {
		predicates = append(predicates, where)
	}
	if opts.DistanceThreshold != nil {
		predicates = append(predicates, fmt.Sprintf("%s <= $%d", distance, next))
		next++
		args = append(args, *opts.DistanceThreshold)
	}
	if opts.ScoreThreshold != nil {
		predicates = append(predicates, fmt.Sprintf("%s >= $%d", scoreSQL(distance, p.Metric), next))
		next++
		args = append(args, *opts.ScoreThreshold)
	}
	columns := append(c.columns("t", opts.IncludeVectors), distance+" AS __vs_distance")
	query := "SELECT " + strings.Join(columns, ",") + " FROM " + c.table() + " t"
	if len(predicates) > 0 {
		query += " WHERE " + strings.Join(predicates, " AND ")
	}
	query += " ORDER BY __vs_distance ASC" + fmt.Sprintf(" LIMIT $%d OFFSET $%d", next, next+1)
	args = append(args, top, opts.Skip)
	if relaxed {
		query = "SELECT * FROM (" + query + ") AS __vs_nearest ORDER BY __vs_distance + 0 ASC"
	}
	return query, args, p, nil
}
func (c *ModelCollection) SearchIter(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return func(yield func(vectordata.ModelSearchResult, error) bool) {
		sql, args, p, err := c.searchPlan(vector, top, opts)
		if err != nil {
			yield(vectordata.ModelSearchResult{}, c.operation("search", err))
			return
		}
		c.runSearch(ctx, sql, args, p, opts, false, yield)
	}
}
func (c *ModelCollection) Search(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	results := []vectordata.ModelSearchResult{}
	for r, err := range c.SearchIter(ctx, vector, top, opts) {
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}
func (c *ModelCollection) SearchText(ctx context.Context, text string, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return c.SearchInput(ctx, text, top, opts)
}
func (c *ModelCollection) SearchInput(ctx context.Context, input any, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return iteration.Collect(c.SearchInputIter(ctx, input, top, opts))
}
func (c *ModelCollection) embedSearchInput(ctx context.Context, input any, top int, opts vectordata.ModelSearchOptions) ([]float32, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return nil, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return nil, err
	}
	if err := vectordata.ValidateModelFilter(opts.Filter, c.definition); err != nil {
		return nil, err
	}
	if _, _, _, err := modelVectorTuning(opts.ProviderOptions, p); err != nil {
		return nil, err
	}
	vector, err := vectordata.EmbedInputQuery(ctx, input, p, c.options)
	if err != nil {
		return nil, c.operation("embed_query", err)
	}
	return vector, nil
}

type scoreScanner struct {
	row   modelScanner
	score *float64
}

func (s scoreScanner) Scan(targets ...any) error { return s.row.Scan(append(targets, s.score)...) }
func (c *ModelCollection) runSearch(ctx context.Context, sql string, args []any, p vectordata.VectorProperty, opts vectordata.ModelSearchOptions, fusion bool, yield func(vectordata.ModelSearchResult, error) bool) {
	report := func(err error) { yield(vectordata.ModelSearchResult{}, c.operation("search", err)) }
	settings, iterative, _, err := modelVectorTuning(opts.ProviderOptions, p)
	if err != nil {
		report(err)
		return
	}
	var conn searchQueryer = c.store.pool
	var tx pgx.Tx
	if len(settings) > 0 {
		tx, err = c.store.pool.Begin(ctx)
		if err != nil {
			report(err)
			return
		}
		defer rollbackTx(ctx, tx)
		conn = tx
		if iterative {
			var version string
			if err := tx.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname='vector'").Scan(&version); err != nil {
				report(err)
				return
			}
			if !supportsIterativeScans(version) {
				report(fmt.Errorf("%w: iterative scans require pgvector 0.8+", vectordata.ErrInvalidSearchOptions))
				return
			}
		}
		for _, s := range settings {
			if _, err := tx.Exec(ctx, "SELECT pg_catalog.set_config($1,$2,true)", s.name, s.value); err != nil {
				report(err)
				return
			}
		}
	}
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		report(err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			report(err)
			return
		}
		var scanner modelScanner = rows
		var score float64
		if fusion {
			scanner = scoreScanner{rows, &score}
		}
		r, distance, err := c.scan(scanner, opts.IncludeVectors, true)
		if err != nil {
			report(err)
			return
		}
		kind := vectordata.ScoreSimilarity
		if fusion {
			kind = vectordata.ScoreRRF
		} else {
			score = vectordata.ScoreFromDistance(p.Metric, distance)
		}
		if !yield(vectordata.ModelSearchResult{Record: r, Distance: distance, Score: score, ScoreKind: kind}, nil) {
			return
		}
	}
	if err := rows.Err(); err != nil {
		report(err)
	} else if err := ctx.Err(); err != nil {
		report(err)
	}
}

type hybridSearchConfig struct {
	vector               vectordata.VectorProperty
	textProperty         string
	candidates, constant int
}

// validateHybridOptions must run before calling an embedder or accessing storage.
func (c *ModelCollection) validateHybridOptions(keywords string, top int, opts vectordata.HybridSearchOptions) (hybridSearchConfig, error) {
	var config hybridSearchConfig
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return config, err
	}
	config.vector = p
	if err := vectordata.ValidateModelSearchOptions(top, opts.ModelSearchOptions); err != nil {
		return config, err
	}
	for _, d := range c.definition.Data {
		if d.IsFullTextIndexed && (opts.TextProperty == d.Name || opts.TextProperty == "") {
			if config.textProperty != "" && opts.TextProperty == "" {
				return config, fmt.Errorf("%w: specify a full-text property", vectordata.ErrInvalidSearchOptions)
			}
			config.textProperty = d.StorageName
		}
	}
	if config.textProperty == "" || strings.TrimSpace(keywords) == "" {
		return config, fmt.Errorf("%w: indexed text property and keywords required", vectordata.ErrInvalidSearchOptions)
	}
	config.candidates = opts.CandidateLimit
	if config.candidates == 0 {
		config.candidates = max(100, top+opts.Skip)
	}
	if config.candidates < top+opts.Skip {
		return config, fmt.Errorf("%w: candidate limit must cover top and skip", vectordata.ErrInvalidSearchOptions)
	}
	config.constant = opts.RRFConstant
	if config.constant == 0 {
		config.constant = 60
	}
	if config.constant < 1 {
		return config, fmt.Errorf("%w: RRF constant must be positive", vectordata.ErrInvalidSearchOptions)
	}
	if err := vectordata.ValidateModelFilter(opts.Filter, c.definition); err != nil {
		return config, err
	}
	if _, _, _, err := modelVectorTuning(opts.ProviderOptions, p); err != nil {
		return config, err
	}
	return config, nil
}
func (c *ModelCollection) HybridSearchText(ctx context.Context, text, keywords string, top int, opts vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return c.HybridSearchInput(ctx, text, keywords, top, opts)
}
func (c *ModelCollection) HybridSearchInput(ctx context.Context, input any, keywords string, top int, opts vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return iteration.Collect(c.HybridSearchInputIter(ctx, input, keywords, top, opts))
}
func (c *ModelCollection) HybridSearch(ctx context.Context, vector []float32, keywords string, top int, opts vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return iteration.Collect(c.HybridSearchIter(ctx, vector, keywords, top, opts))
}
func (c *ModelCollection) hybridPlan(vector []float32, keywords string, top int, opts vectordata.HybridSearchOptions) (string, []any, vectordata.VectorProperty, error) {
	config, err := c.validateHybridOptions(keywords, top, opts)
	p := config.vector
	if err != nil {
		return "", nil, p, err
	}
	vector, err = vectordata.NormalizeModelVector(vector, p)
	if err != nil {
		return "", nil, p, err
	}
	return c.hybridValuePlan(vectorLiteral(vector), keywords, top, opts)
}
func (c *ModelCollection) hybridValuePlan(literal any, keywords string, top int, opts vectordata.HybridSearchOptions) (string, []any, vectordata.VectorProperty, error) {
	config, err := c.validateHybridOptions(keywords, top, opts)
	p := config.vector
	if err != nil {
		return "", nil, p, err
	}
	where, filterArgs, next, err := filtersql.CompileFilterSQL(opts.Filter, c.filterConfig(), 3)
	if err != nil {
		return "", nil, p, err
	}
	args := append([]any{literal, keywords}, filterArgs...)
	bind := func(value any) string { ph := fmt.Sprintf("$%d", next); next++; args = append(args, value); return ph }
	op, _ := modelMetricOperator(p)
	distance := "(t." + quoteIdent(p.StorageName) + " " + op + " $1::" + vectorQuerySQLType(p) + ")"
	key := "t." + quoteIdent(c.definition.Key.StorageName)
	predicate := "t." + quoteIdent(p.StorageName) + " IS NOT NULL"
	if where != "" {
		predicate += " AND (" + where + ")"
	}
	text := ftsExpr("t." + quoteIdent(config.textProperty))
	query := "plainto_tsquery('simple'::regconfig,$2)"
	limit := bind(config.candidates)
	rrf := bind(config.constant)
	sql := `WITH __vs_vector AS MATERIALIZED (SELECT ` + key + ` AS __vs_key, ` + distance + ` AS __vs_distance FROM ` + c.table() + ` t WHERE ` + predicate + ` ORDER BY __vs_distance ASC LIMIT ` + limit + `),
 __vs_text AS MATERIALIZED (SELECT ` + key + ` AS __vs_key, ts_rank_cd(` + text + `,` + query + `) AS __vs_rank FROM ` + c.table() + ` t WHERE (` + predicate + `) AND ` + text + ` @@ ` + query + ` ORDER BY __vs_rank DESC, ` + key + ` LIMIT ` + limit + `),
 __vs_ranks AS (SELECT __vs_key, row_number() OVER (ORDER BY __vs_distance, __vs_key) AS __vs_rank FROM __vs_vector UNION ALL SELECT __vs_key, row_number() OVER (ORDER BY __vs_rank DESC, __vs_key) FROM __vs_text),
 __vs_fused AS (SELECT __vs_key, sum(1.0 / (` + rrf + `::float8 + __vs_rank)) AS __vs_score FROM __vs_ranks GROUP BY __vs_key)
 SELECT ` + strings.Join(c.columns("t", opts.IncludeVectors), ",") + `,` + distance + `, f.__vs_score FROM __vs_fused f JOIN ` + c.table() + ` t ON ` + key + `=f.__vs_key`
	thresholds := []string{}
	if opts.DistanceThreshold != nil {
		thresholds = append(thresholds, distance+" <= "+bind(*opts.DistanceThreshold))
	}
	if opts.ScoreThreshold != nil {
		thresholds = append(thresholds, "f.__vs_score >= "+bind(*opts.ScoreThreshold))
	}
	if len(thresholds) > 0 {
		sql += " WHERE " + strings.Join(thresholds, " AND ")
	}
	sql += " ORDER BY f.__vs_score DESC, " + key + " LIMIT " + bind(top) + " OFFSET " + bind(opts.Skip)
	return sql, args, p, nil
}
