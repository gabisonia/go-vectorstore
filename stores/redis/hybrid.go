package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sort"
	"strconv"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

var _ vectordata.HybridSearcher = (*Collection)(nil)
var _ vectordata.InputHybridSearcher = (*Collection)(nil)
var _ vectordata.HybridSearchIterator = (*Collection)(nil)
var _ vectordata.InputHybridSearchIterator = (*Collection)(nil)

type hybridConfig struct {
	opts                                 vectordata.HybridSearchOptions
	vector                               vectordata.VectorProperty
	tuning                               SearchTuning
	textAlias, vectorAlias, keywordQuery string
	keywordParams                        []any
	candidates, constant                 int
}

func (c *Collection) validateHybrid(ctx context.Context, keywords string, top int, opts vectordata.HybridSearchOptions) (hybridConfig, error) {
	var cfg hybridConfig
	if err := ctx.Err(); err != nil {
		return cfg, err
	}
	if !c.store.nativeIndexes {
		return cfg, vectordata.ErrUnsupported
	}
	opts.KeywordTerms = slices.Clone(opts.KeywordTerms)
	p, err := c.validateSearch(top, opts.ModelSearchOptions)
	if err != nil {
		return cfg, err
	}
	cfg.vector = p
	cfg.tuning, err = c.searchTuning(opts.ModelSearchOptions)
	if err != nil {
		return cfg, err
	}
	for i, v := range c.definition.Vectors {
		if v.Name == p.Name {
			cfg.vectorAlias = "v" + strconv.Itoa(i)
		}
	}
	for i, d := range c.definition.Data {
		if nativeText(d) && (opts.TextProperty == "" || opts.TextProperty == d.Name) {
			if cfg.textAlias != "" && opts.TextProperty == "" {
				return cfg, fmt.Errorf("%w: specify a full-text property", vectordata.ErrInvalidSearchOptions)
			}
			cfg.textAlias = "text" + strconv.Itoa(i)
		}
	}
	if cfg.textAlias == "" {
		return cfg, fmt.Errorf("%w: indexed text property required", vectordata.ErrInvalidSearchOptions)
	}
	cfg.keywordQuery, cfg.keywordParams, err = hybridKeywords(keywords, opts.KeywordTerms)
	if err != nil {
		return cfg, err
	}
	cfg.candidates = opts.CandidateLimit
	if cfg.candidates == 0 {
		cfg.candidates = max(100, top+opts.Skip)
	}
	if cfg.candidates < top+opts.Skip || cfg.candidates > nativeMaxDocuments {
		return cfg, fmt.Errorf("%w: candidate limit must cover top/skip and be at most %d", vectordata.ErrInvalidSearchOptions, nativeMaxDocuments)
	}
	cfg.constant = opts.RRFConstant
	if cfg.constant == 0 {
		cfg.constant = 60
	}
	if cfg.constant < 1 {
		return cfg, fmt.Errorf("%w: RRF constant must be positive", vectordata.ErrInvalidSearchOptions)
	}
	opts.Filter, err = exactsearch.FreezeFilter(opts.Filter, c.definition)
	if err != nil {
		return cfg, err
	}
	if opts.DistanceThreshold != nil {
		v := *opts.DistanceThreshold
		opts.DistanceThreshold = &v
	}
	if opts.ScoreThreshold != nil {
		v := *opts.ScoreThreshold
		opts.ScoreThreshold = &v
	}
	cfg.opts = opts
	return cfg, nil
}

func (c *Collection) hybridManifest(ctx context.Context) (hashManifest, []byte, error) {
	m, raw, indexed, err := c.nativeManifest(ctx)
	if err != nil {
		return m, nil, err
	}
	if !indexed || m.Format == 2 && m.IndexVersion != 2 || m.Format == 3 && m.IndexVersion != 5 {
		return m, nil, fmt.Errorf("%w: hybrid search requires a current TEXT index; explicitly rebuild native indexes", vectordata.ErrUnsupported)
	}
	return m, raw, nil
}

const hybridProbe = nativeReadyLua + `
if redis.call('GET',KEYS[1])~=ARGV[1] then return {0} end
if redis.call('GET',KEYS[2])~=ARGV[2] then return {-1} end
return {ready(KEYS[3],ARGV[3],ARGV[4],ARGV[5],ARGV[6])}`

func (c *Collection) hybridHeaderArgs(m hashManifest, raw []byte) ([]string, []any) {
	fields, _ := c.nativeFields(m)
	schema, _ := json.Marshal(fields)
	kind := "HASH"
	if m.Format == 3 {
		kind = "JSON"
	}
	prefix := c.nativeGenerationPrefix(m)
	return []string{c.store.key(c.name), prefix + "lease", m.Index}, []any{raw, m.Revision, prefix, string(schema), len(m.Tokens), kind}
}
func hybridStatus(result []any) error {
	status, err := nativeStatus(result)
	if err != nil {
		return err
	}
	switch status {
	case 1:
		return nil
	case 0:
		return errManifestChanged
	case 2:
		return fmt.Errorf("%w: hybrid TEXT index is missing or unready; explicitly rebuild native indexes", vectordata.ErrUnsupported)
	default:
		return nativeStatusError(status)
	}
}
func (c *Collection) hybridPreflight(ctx context.Context) error {
	for range 64 {
		m, raw, err := c.hybridManifest(ctx)
		if err != nil {
			return err
		}
		keys, args := c.hybridHeaderArgs(m, raw)
		result, err := c.store.client.Eval(ctx, hybridProbe, keys, args...).Slice()
		if err != nil {
			return err
		}
		err = hybridStatus(result)
		if errors.Is(err, errManifestChanged) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w: Redis hybrid read contention", vectordata.ErrInvalidRecord)
}

type hybridRank struct {
	record                   vectordata.ModelRecord
	distance, keyword, score float64
}

func (c *Collection) rankHybrid(ctx context.Context, m hashManifest, cfg hybridConfig, vector []float32, rows []vectordata.ModelRecord, ids []string, scores map[string]float64, top int, scan bool) ([]vectordata.ModelSearchResult, bool, error) {
	vectors := map[string]bool{}
	for _, id := range ids {
		vectors[id] = true
	}
	var vr, tr []*hybridRank
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		match, err := vectordata.MatchModelFilter(cfg.opts.Filter, row, c.definition)
		if err != nil {
			return nil, false, err
		}
		if !match || !vectordata.HasModelVector(row, cfg.vector) {
			continue
		}
		hit := &hybridRank{record: row, distance: exactsearch.DenseDistance(vector, row.Vectors[cfg.vector.Name], cfg.vector.Metric)}
		id := c.nativeGenerationPrefix(m) + "r:" + encodeName(token(row.Key))
		if vectors[id] {
			vr = append(vr, hit)
		}
		if score, ok := scores[id]; ok {
			hit.keyword = score
			tr = append(tr, hit)
		}
	}
	lessKey := func(a, b *hybridRank) bool { return vectordata.CompareModelValues(a.record.Key, b.record.Key) < 0 }
	sort.Slice(vr, func(i, j int) bool {
		if vr[i].distance != vr[j].distance {
			return vr[i].distance < vr[j].distance
		}
		return lessKey(vr[i], vr[j])
	})
	sort.Slice(tr, func(i, j int) bool {
		if tr[i].keyword != tr[j].keyword {
			return tr[i].keyword > tr[j].keyword
		}
		return lessKey(tr[i], tr[j])
	})
	// Resolve HNSW underfill/boundary ties with a fresh guarded full scan. Both
	// lists are fetched again together, so a concurrent publication cannot mix them.
	if !scan && cfg.vector.Index.Method == vectordata.IndexMethodHNSW && (len(vr) < cfg.candidates || len(vr) > 0 && vr[min(cfg.candidates, len(vr))-1].distance >= vr[len(vr)-1].distance) {
		return nil, true, nil
	}
	fused := map[string]*hybridRank{}
	for _, list := range [][]*hybridRank{vr, tr} {
		for i, hit := range list[:min(cfg.candidates, len(list))] {
			id := token(hit.record.Key)
			existing := fused[id]
			if existing == nil {
				existing = hit
				fused[id] = hit
			}
			existing.score += 1 / (float64(cfg.constant) + float64(i+1))
		}
	}
	ranked := make([]*hybridRank, 0, len(fused))
	for _, hit := range fused {
		if cfg.opts.DistanceThreshold != nil && hit.distance > *cfg.opts.DistanceThreshold || cfg.opts.ScoreThreshold != nil && hit.score < *cfg.opts.ScoreThreshold {
			continue
		}
		ranked = append(ranked, hit)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return lessKey(ranked[i], ranked[j])
	})
	start := min(cfg.opts.Skip, len(ranked))
	end := start + min(top, len(ranked)-start)
	out := make([]vectordata.ModelSearchResult, 0, end-start)
	for _, hit := range ranked[start:end] {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		out = append(out, vectordata.ModelSearchResult{Record: vectordata.CloneModelRecord(hit.record, c.definition, cfg.opts.IncludeVectors), Distance: hit.distance, Score: hit.score, ScoreKind: vectordata.ScoreRRF})
	}
	return out, false, ctx.Err()
}
func (c *Collection) runHybrid(ctx context.Context, vector []float32, top int, cfg hybridConfig) ([]vectordata.ModelSearchResult, error) {
	vector, err := vectordata.NormalizeModelVector(vector, cfg.vector)
	if err != nil {
		return nil, err
	}
	_, supported, err := c.compileNativeFilter(cfg.opts.Filter)
	if err != nil {
		return nil, err
	}
	scan := cfg.tuning.Exact || !supported || !nativeVectorProperty(cfg.vector) || !nativeVectorSafe(vector)
	for range 64 {
		m, raw, err := c.hybridManifest(ctx)
		if err != nil {
			return nil, err
		}
		indexed := false
		for _, name := range m.IndexVectors {
			if name == cfg.vector.Name {
				indexed = true
			}
		}
		full := scan || !indexed || len(m.Tokens) == 0
		k := len(m.Tokens)
		if !full && cfg.vector.Index.Method == vectordata.IndexMethodHNSW {
			k = min(k, max(64, 4*cfg.candidates))
		}
		rows, ids, scores, err := c.hybridRead(ctx, m, raw, cfg, vector, full, k)
		if errors.Is(err, errManifestChanged) {
			continue
		}
		if err != nil {
			return nil, err
		}
		results, retry, err := c.rankHybrid(ctx, m, cfg, vector, rows, ids, scores, top, full)
		if retry {
			scan = true
			continue
		}
		return results, err
	}
	return nil, fmt.Errorf("%w: Redis hybrid read contention", vectordata.ErrInvalidRecord)
}
func (c *Collection) HybridSearch(ctx context.Context, vector []float32, keywords string, top int, opts vectordata.HybridSearchOptions) (results []vectordata.ModelSearchResult, err error) {
	defer func() { err = c.operation(ctx, "hybrid_search", err) }()
	cfg, err := c.validateHybrid(ctx, keywords, top, opts)
	if err != nil {
		return nil, err
	}
	return c.runHybrid(ctx, vector, top, cfg)
}
func (c *Collection) HybridSearchText(ctx context.Context, text, keywords string, top int, opts vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return c.HybridSearchInput(ctx, text, keywords, top, opts)
}
func (c *Collection) HybridSearchInput(ctx context.Context, input any, keywords string, top int, opts vectordata.HybridSearchOptions) (results []vectordata.ModelSearchResult, err error) {
	defer func() { err = c.operation(ctx, "hybrid_search_input", err) }()
	cfg, err := c.validateHybrid(ctx, keywords, top, opts)
	if err != nil {
		return nil, err
	}
	if err = c.hybridPreflight(ctx); err != nil {
		return nil, err
	}
	vector, err := vectordata.EmbedInputQuery(ctx, input, cfg.vector, c.options)
	if err != nil {
		return nil, err
	}
	return c.runHybrid(ctx, vector, top, cfg)
}
func (c *Collection) HybridSearchIter(ctx context.Context, vector []float32, keywords string, top int, opts vectordata.HybridSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelSearchResult, error) {
		return c.HybridSearch(ctx, vector, keywords, top, opts)
	})
}
func (c *Collection) HybridSearchTextIter(ctx context.Context, text, keywords string, top int, opts vectordata.HybridSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.HybridSearchInputIter(ctx, text, keywords, top, opts)
}
func (c *Collection) HybridSearchInputIter(ctx context.Context, input any, keywords string, top int, opts vectordata.HybridSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelSearchResult, error) {
		return c.HybridSearchInput(ctx, input, keywords, top, opts)
	})
}
