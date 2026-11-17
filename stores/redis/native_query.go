package redis

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

// SearchTuning controls Redis search. EFRuntime applies only to HNSW; positive
// values on FLAT are rejected. Distances/scores are remeasured in Go. Exact
// bypasses native search for comparison/reproducibility, including FLAT indexes.
type SearchTuning struct {
	EFRuntime int
	Exact     bool
}

func (c *Collection) searchTuning(opts vectordata.ModelSearchOptions) (SearchTuning, error) {
	if opts.ProviderOptions == nil {
		return SearchTuning{}, nil
	}
	t, ok := opts.ProviderOptions.(SearchTuning)
	if !ok {
		return t, vectordata.ErrUnsupported
	}
	if t.EFRuntime < 0 || t.EFRuntime > 10000 || t.Exact && t.EFRuntime != 0 {
		return t, vectordata.ErrInvalidSearchOptions
	}
	if t.EFRuntime > 0 && !c.store.nativeIndexes {
		return t, vectordata.ErrUnsupported
	}
	if t.EFRuntime > 0 {
		p, err := c.definition.Vector(opts.VectorProperty)
		if err != nil {
			return t, err
		}
		if p.Index != nil && p.Index.Method == vectordata.IndexMethodFlat {
			return t, vectordata.ErrInvalidSearchOptions
		}
	}
	return t, nil
}

const nativeExecute = nativeReadyLua + `
if redis.call('GET',KEYS[1])~=ARGV[1] then return {0} end
if redis.call('GET',KEYS[2])~=ARGV[2] then return {-1} end
local status=ready(KEYS[3],ARGV[3],ARGV[4],ARGV[5])
if type(status)=='table' and status.err then return status end
if status~=1 then return {status} end
return {1,redis.call('FT.SEARCH',KEYS[3],unpack(ARGV,6))}`

// RedisJSON RETURN values use JSON encoding. Fetch wrappers after NOCONTENT in
// the same execution instead, preserving the opaque canonical payload string.
const nativeJSONExecute = nativeReadyLua + `
if redis.call('GET',KEYS[1])~=ARGV[1] then return {0} end
if redis.call('GET',KEYS[2])~=ARGV[2] then return {-1} end
local status=ready(KEYS[3],ARGV[3],ARGV[4],ARGV[5],'JSON')
if status~=1 then return {status} end
local found=redis.call('FT.SEARCH',KEYS[3],unpack(ARGV,6))
local result={found[1]}
for i=2,#found do
 local key=text(found[i])
 local raw=redis.call('JSON.GET',key)
 if not raw then return {-1} end
 local ok,row=pcall(cjson.decode,raw)
 if not ok or type(row)~='table' or row.__vs_revision~=ARGV[2] or type(row.__vs_token)~='string' or type(row.__vs_payload)~='string' then return {-1} end
 result[#result+1]=key
 result[#result+1]={'__vs_payload',row.__vs_payload,'__vs_token',row.__vs_token,'__vs_revision',row.__vs_revision}
end
return {1,result}`

func (c *Collection) nativeManifest(ctx context.Context) (hashManifest, []byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return hashManifest{}, nil, false, err
	}
	raw, err := c.store.client.Get(ctx, c.store.key(c.name)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return hashManifest{}, nil, false, vectordata.ErrCollectionNotFound
	}
	if err != nil {
		return hashManifest{}, nil, false, err
	}
	if len(raw) > c.store.maxBytes {
		return hashManifest{}, nil, false, vectordata.ErrInvalidRecord
	}
	var m hashManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return m, nil, false, fmt.Errorf("%w: malformed Redis manifest", vectordata.ErrInvalidRecord)
	}
	if m.Definition != c.registration || (m.Format != 1 && m.Format != 2 && m.Format != 3) {
		return m, nil, false, vectordata.ErrSchemaMismatch
	}
	if m.Format == 1 {
		return m, raw, false, nil
	}
	if !validRevision(m.Revision) || m.Next < 0 || m.Tokens == nil {
		return m, nil, false, vectordata.ErrInvalidRecord
	}
	seen := map[string]bool{}
	for _, t := range m.Tokens {
		if t == "" || seen[t] {
			return m, nil, false, vectordata.ErrInvalidRecord
		}
		seen[t] = true
	}
	if m.Index == "" || ((m.Format == 2 && m.IndexVersion != 1 && m.IndexVersion != 2) || (m.Format == 3 && m.IndexVersion != 3)) || len(m.Tokens) > nativeMaxDocuments {
		return m, raw, false, nil
	}
	if m.Index != c.nativeIndexName(m.Format, m.Revision) {
		return m, nil, false, vectordata.ErrSchemaMismatch
	}
	if _, err = c.nativeFields(m); err != nil {
		return m, nil, false, err
	}
	return m, raw, true, nil
}

func (c *Collection) nativeReply(ctx context.Context, m hashManifest, raw []byte, plan nativeFilterPlan, limit int, knn string, blob []byte, ef int) ([]any, bool, error) {
	fields, err := c.nativeFields(m)
	if err != nil {
		return nil, false, err
	}
	schema, _ := json.Marshal(fields)
	params := append([]any(nil), plan.params...)
	query := plan.query
	if knn != "" {
		query = "(" + query + ")=>[KNN $__k @" + knn + " $__q"
		params = append(params, "__k", strconv.Itoa(limit), "__q", string(blob))
		if ef > 0 {
			query += " EF_RUNTIME $__ef"
			params = append(params, "__ef", strconv.Itoa(ef))
		}
		query += " AS __vs_distance]"
	}
	args := []any{raw, m.Revision, c.nativeGenerationPrefix(m), string(schema), len(m.Tokens), query}
	if len(params) > 0 {
		args = append(args, "PARAMS", len(params))
		args = append(args, params...)
	}
	if knn != "" {
		args = append(args, "SORTBY", "__vs_distance", "ASC")
	}
	script := nativeExecute
	if m.Format == 3 {
		script = nativeJSONExecute
		args = append(args, "NOCONTENT")
	} else {
		args = append(args, "RETURN", 3, "__vs_payload", "__vs_token", "__vs_revision")
	}
	args = append(args, "LIMIT", 0, limit, "DIALECT", 2, "TIMEOUT", 0)
	result, err := c.store.client.Eval(ctx, script, []string{c.store.key(c.name), c.nativeGenerationPrefix(m) + "lease", m.Index}, args...).Slice()
	if err != nil {
		return nil, false, err
	}
	status, err := nativeStatus(result)
	if err != nil {
		return nil, false, err
	}
	if status == 0 {
		return nil, false, errManifestChanged
	}
	if status == 2 {
		return nil, false, nil
	}
	if status != 1 {
		return nil, false, nativeStatusError(status)
	}
	if len(result) != 2 {
		return nil, false, vectordata.ErrInvalidRecord
	}
	reply, ok := result[1].([]any)
	if !ok || len(reply) == 0 {
		return nil, false, vectordata.ErrInvalidRecord
	}
	return reply, true, nil
}
func nativeTotal(reply []any) (int, error) {
	if len(reply) == 0 {
		return 0, vectordata.ErrInvalidRecord
	}
	n, ok := reply[0].(int64)
	if !ok || n < 0 || n > nativeMaxDocuments {
		return 0, vectordata.ErrInvalidRecord
	}
	return int(n), nil
}
func (c *Collection) nativeRecords(ctx context.Context, m hashManifest, reply []any) ([]vectordata.ModelRecord, error) {
	if len(reply)%2 != 1 {
		return nil, vectordata.ErrInvalidRecord
	}
	members := map[string]bool{}
	for _, t := range m.Tokens {
		members[t] = true
	}
	state := snapshot{Records: map[string]json.RawMessage{}}
	size := 0
	for i := 1; i < len(reply); i += 2 {
		key, ok := reply[i].(string)
		if !ok {
			return nil, vectordata.ErrInvalidRecord
		}
		flat, ok := reply[i+1].([]any)
		if !ok || len(flat)%2 != 0 {
			return nil, vectordata.ErrInvalidRecord
		}
		values := map[string]string{}
		for j := 0; j < len(flat); j += 2 {
			name, ok := flat[j].(string)
			if !ok {
				return nil, vectordata.ErrInvalidRecord
			}
			value, ok := flat[j+1].(string)
			if !ok {
				return nil, vectordata.ErrInvalidRecord
			}
			values[name] = value
		}
		t := values["__vs_token"]
		if !members[t] || values["__vs_revision"] != m.Revision || key != c.nativeGenerationPrefix(m)+"r:"+encodeName(t) || values["__vs_payload"] == "" {
			return nil, vectordata.ErrInvalidRecord
		}
		if _, exists := state.Records[t]; exists {
			return nil, vectordata.ErrInvalidRecord
		}
		size += len(key) + len(values["__vs_payload"])
		if size > c.store.maxBytes {
			return nil, vectordata.ErrInvalidRecord
		}
		state.Records[t] = json.RawMessage(values["__vs_payload"])
	}
	return c.decode(ctx, state, nil)
}
func (c *Collection) nativeQuery(ctx context.Context, filter vectordata.Filter, top int, opts vectordata.QueryOptions, count bool) ([]vectordata.ModelRecord, int64, bool, error) {
	plan, ok, err := c.compileNativeFilter(filter)
	if err != nil || !ok {
		return nil, 0, false, err
	}
	for attempt := 0; attempt < 64; attempt++ {
		m, raw, indexed, err := c.nativeManifest(ctx)
		if err != nil || !indexed {
			return nil, 0, false, err
		}
		if m.Format == 3 {
			return nil, 0, false, nil
		}
		limit := len(m.Tokens)
		if count {
			limit = 0
		}
		reply, used, err := c.nativeReply(ctx, m, raw, plan, limit, "", nil, 0)
		if errors.Is(err, errManifestChanged) {
			continue
		}
		if err != nil || !used {
			return nil, 0, false, err
		}
		total, err := nativeTotal(reply)
		if err != nil || total > len(m.Tokens) {
			return nil, 0, true, vectordata.ErrInvalidRecord
		}
		if count {
			if err := ctx.Err(); err != nil {
				return nil, 0, true, err
			}
			return nil, int64(total), true, nil
		}
		if (len(reply)-1)/2 != total {
			return nil, 0, false, nil
		} // server result cap: preserve full semantics through Go fallback
		rows, err := c.nativeRecords(ctx, m, reply)
		if err != nil {
			return nil, 0, true, err
		}
		engine, err := c.engineForNative(ctx, rows)
		if err != nil {
			return nil, 0, true, err
		}
		rows, err = engine.Query(ctx, nil, top, opts)
		return rows, 0, true, err
	}
	return nil, 0, true, fmt.Errorf("%w: native Redis read contention", vectordata.ErrInvalidRecord)
}
func (c *Collection) engineForNative(ctx context.Context, rows []vectordata.ModelRecord) (*memory.Collection, error) {
	base, err := memory.New().EnsureModelCollection(ctx, c.name, c.readDefinition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		return nil, err
	}
	if _, err = base.Insert(ctx, rows); err != nil {
		return nil, err
	}
	return base.(*memory.Collection), nil
}

func (c *Collection) nativeSearch(ctx context.Context, vector []float32, p vectordata.VectorProperty, top int, opts vectordata.ModelSearchOptions, tuning SearchTuning) ([]vectordata.ModelSearchResult, bool, error) {
	if tuning.Exact || !nativeVectorProperty(p) || !nativeVectorSafe(vector) {
		return nil, false, nil
	}
	plan, ok, err := c.compileNativeFilter(opts.Filter)
	if err != nil || !ok {
		return nil, false, err
	}
	blob := make([]byte, len(vector)*4)
	for i, x := range vector {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(x))
	}
	alias := ""
	for i, v := range c.definition.Vectors {
		if v.Name == p.Name {
			alias = "v" + strconv.Itoa(i)
		}
	}
	flat := p.Index.Method == vectordata.IndexMethodFlat
	ef := 0
	if !flat {
		ef = tuning.EFRuntime
		if ef == 0 {
			ef = 128
		}
	}
	for attempt := 0; attempt < 64; attempt++ {
		m, raw, indexed, err := c.nativeManifest(ctx)
		if err != nil || !indexed {
			return nil, false, err
		}
		if m.Format == 3 && opts.Filter != nil {
			return nil, false, nil
		}
		available := false
		for _, name := range m.IndexVectors {
			if name == p.Name {
				available = true
			}
		}
		if !available {
			return nil, false, nil
		}
		n := len(m.Tokens)
		wanted := top + opts.Skip
		if wanted > nativeMaxDocuments {
			return nil, false, nil
		}
		k := wanted
		if k <= n/4 {
			k *= 4
		} else {
			k = n
		}
		if k < 64 {
			k = min(64, n)
		}
		if flat {
			// Redis scores FLOAT32 vectors with different arithmetic from Go.
			// Retrieve every matching vector so roundoff cannot discard a nearer
			// canonical record or a native-key tie before exact Go selection.
			k = n
		}
		if k == 0 {
			_, _, used, err := c.nativeQuery(ctx, nil, 1, vectordata.QueryOptions{}, true)
			return []vectordata.ModelSearchResult{}, used, err
		}
		changed := false
		for {
			reply, used, err := c.nativeReply(ctx, m, raw, plan, k, alias, blob, ef)
			if errors.Is(err, errManifestChanged) {
				changed = true
				break
			}
			if err != nil || !used {
				return nil, false, err
			}
			total, err := nativeTotal(reply)
			if err != nil || (len(reply)-1)/2 != total {
				return nil, true, vectordata.ErrInvalidRecord
			}
			rows, err := c.nativeRecords(ctx, m, reply)
			if err != nil {
				return nil, true, err
			}
			clean := opts
			clean.ProviderOptions = nil
			results, err := exactsearch.Run(ctx, c.definition, p, top, clean, iteration.FromSlice(ctx, func() ([]vectordata.ModelRecord, error) { return rows, nil }), func(r vectordata.ModelRecord) (float64, error) {
				return exactsearch.DenseDistance(vector, r.Vectors[p.Name], p.Metric), nil
			})
			if err != nil {
				return nil, true, err
			}
			if flat {
				return results, true, nil
			}
			expand := len(results) < top
			if len(results) > 0 && len(rows) > 0 {
				distances := make([]float64, 0, len(rows))
				for _, r := range rows {
					if v := r.Vectors[p.Name]; v != nil {
						distances = append(distances, exactsearch.DenseDistance(vector, v, p.Metric))
					}
				}
				sort.Float64s(distances)
				if len(distances) > 0 && results[len(results)-1].Distance >= distances[len(distances)-1] {
					expand = true
				}
			}
			if !expand {
				return results, true, nil
			}
			// HNSW can underfill a large K or omit equal-distance neighbors. An
			// unresolved boundary must use the complete canonical snapshot to
			// preserve native-key tie ordering and threshold/paging semantics.
			if k >= n || total < k {
				return nil, false, nil
			}
			k = min(n, k*2)
		}
		if !changed {
			break
		}
	}
	return nil, true, fmt.Errorf("%w: native Redis search contention", vectordata.ErrInvalidRecord)
}
