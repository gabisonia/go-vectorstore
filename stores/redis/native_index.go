package redis

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

const nativeMaxDimensions = 4096
const nativeMaxDocuments = 1000000
const nativeTagLimit = 1024
const nativeMaxTextFields = 128

type nativeField struct {
	Name, Alias, Kind, Metric, Algorithm string
	Dimensions, M, EFConstruction        int
}

func nativeVectorProperty(p vectordata.VectorProperty) bool {
	if p.Index == nil || p.Dimensions > nativeMaxDimensions {
		return false
	}
	switch p.Index.Method {
	case vectordata.IndexMethodFlat:
	case vectordata.IndexMethodHNSW:
		if p.Index.HNSW.M > 512 || p.Index.HNSW.EfConstruction > 1000000 {
			return false
		}
	default:
		return false
	}
	return p.Metric == vectordata.DistanceCosine || p.Metric == vectordata.DistanceL2 || p.Metric == vectordata.DistanceL2Squared || p.Metric == vectordata.DistanceInnerProduct
}
func nativeVectorSafe(v []float32) bool {
	for _, x := range v {
		a := math.Abs(float64(x))
		if a > 1e10 || a != 0 && a < 1e-10 {
			return false
		}
	}
	return true
}

// Select only projections whose full generation stays in Redis' safe FLOAT32 band.
func (c *Collection) nativeVectors(records []vectordata.ModelRecord) []string {
	var names []string
	for _, p := range c.definition.Vectors {
		if !nativeVectorProperty(p) {
			continue
		}
		safe := true
		for _, r := range records {
			if v := r.Vectors[p.Name]; v != nil && !nativeVectorSafe(v) {
				safe = false
				break
			}
		}
		if safe {
			names = append(names, p.Name)
		}
	}
	return names
}
func (s *Store) jsonIndexPrefix(name string) string {
	return "__vectorstore_redis_json_idx_v1:" + encodeName(s.metadata.Namespace) + ":" + encodeName(name) + ":"
}
func (c *Collection) nativeIndexName(format int, revision string) string {
	if format == 3 {
		return c.store.jsonIndexPrefix(c.name) + revision
	}
	return c.indexName(revision)
}
func (c *Collection) nativeGenerationPrefix(m hashManifest) string {
	if m.Format == 3 {
		return c.jsonGenerationPrefix(m.Revision)
	}
	return c.generationPrefix(m.Revision)
}
func nativeData(p vectordata.DataProperty) bool {
	return p.IsIndexed && (p.Type == vectordata.TypeString || p.Type == vectordata.TypeInt64 || p.Type == vectordata.TypeFloat64 || p.Type == vectordata.TypeBool)
}
func nativeText(p vectordata.DataProperty) bool {
	return p.IsFullTextIndexed && p.Type == vectordata.TypeString
}

// Version 1 accepted full-text hints but built scalar/vector fields only. Keep
// its physical schema readable until an explicit rebuild or a native write.
func (c *Collection) nativeIndexVersion() int {
	for _, p := range c.definition.Data {
		if nativeText(p) {
			return 2
		}
	}
	return 1
}
func nativeTag(v any) (string, bool) {
	var text string
	switch x := v.(type) {
	case string:
		text = x
	case int64:
		text = strconv.FormatInt(x, 10)
	case uint64:
		text = strconv.FormatUint(x, 10)
	case float64:
		if x == 0 {
			x = 0
		}
		text = strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			text = "1"
		} else {
			text = "0"
		}
	default:
		return "", false
	}
	if len(text) > (nativeTagLimit-1)/2 {
		return "", false
	}
	return "x" + hex.EncodeToString([]byte(text)), true
}
func integerParts(v any) (int64, int64, bool) {
	switch x := v.(type) {
	case int64:
		return x >> 32, int64(uint32(x)), true
	case uint64:
		return int64(x >> 32), int64(uint32(x)), true
	}
	return 0, 0, false
}
func (c *Collection) nativeProjection(r vectordata.ModelRecord) []any {
	var out []any
	if tag, ok := nativeTag(token(r.Key)); ok {
		out = append(out, "__vs_key_tag", tag)
	}
	if hi, lo, ok := integerParts(r.Key); ok {
		out = append(out, "__vs_key_hi", strconv.FormatInt(hi, 10), "__vs_key_lo", strconv.FormatInt(lo, 10))
	}
	for i, p := range c.definition.Data {
		if !nativeData(p) {
			continue
		}
		v := r.Data[p.Name]
		if v == nil {
			continue
		}
		suffix := strconv.Itoa(i)
		out = append(out, "__vs_has_"+suffix, "x")
		if tag, ok := nativeTag(v); ok {
			out = append(out, "__vs_tag_"+suffix, tag)
		}
		if hi, lo, ok := integerParts(v); ok {
			out = append(out, "__vs_hi_"+suffix, strconv.FormatInt(hi, 10), "__vs_lo_"+suffix, strconv.FormatInt(lo, 10))
		}
	}
	return out
}
func (s *Store) indexPrefix(name string) string {
	return "__vectorstore_redis_idx_v1:" + encodeName(s.metadata.Namespace) + ":" + encodeName(name) + ":"
}
func (c *Collection) indexName(revision string) string { return c.store.indexPrefix(c.name) + revision }
func (c *Collection) nativeFields(m hashManifest) ([]nativeField, error) {
	fields := []nativeField{{Name: "__vs_revision", Alias: "revision", Kind: "TAG"}, {Name: "__vs_key_tag", Alias: "key", Kind: "TAG"}}
	if c.definition.Key.Type == vectordata.KeyInt64 || c.definition.Key.Type == vectordata.KeyUint64 {
		fields = append(fields, nativeField{Name: "__vs_key_hi", Alias: "kh", Kind: "NUMERIC"}, nativeField{Name: "__vs_key_lo", Alias: "kl", Kind: "NUMERIC"})
	}
	if m.Format == 3 {
		fields = []nativeField{{Name: "$.__vs_revision", Alias: "revision", Kind: "TAG"}}
	}
	textFields := 0
	for i, p := range c.definition.Data {
		if m.Format == 3 {
			continue
		}
		n := strconv.Itoa(i)
		if nativeData(p) {
			fields = append(fields, nativeField{Name: "__vs_has_" + n, Alias: "h" + n, Kind: "TAG"}, nativeField{Name: "__vs_tag_" + n, Alias: "t" + n, Kind: "TAG"})
			if p.Type == vectordata.TypeInt64 {
				fields = append(fields, nativeField{Name: "__vs_hi_" + n, Alias: "hi" + n, Kind: "NUMERIC"}, nativeField{Name: "__vs_lo_" + n, Alias: "lo" + n, Kind: "NUMERIC"})
			}
		}
		if m.IndexVersion == 2 && nativeText(p) {
			textFields++
			fields = append(fields, nativeField{Name: "d_" + encodeName(p.Name), Alias: "text" + n, Kind: "TEXT"})
		}
	}
	if textFields > nativeMaxTextFields {
		return nil, fmt.Errorf("%w: native Redis schema exceeds 128 TEXT fields", vectordata.ErrUnsupported)
	}
	seen := map[string]bool{}
	for _, name := range m.IndexVectors {
		p, err := c.definition.Vector(name)
		if err != nil || !nativeVectorProperty(p) || seen[name] {
			return nil, vectordata.ErrSchemaMismatch
		}
		seen[name] = true
		alias := ""
		for i, v := range c.definition.Vectors {
			if v.Name == name {
				alias = "v" + strconv.Itoa(i)
			}
		}
		metric := "L2"
		if p.Metric == vectordata.DistanceCosine {
			metric = "COSINE"
		} else if p.Metric == vectordata.DistanceInnerProduct {
			metric = "IP"
		}
		path := "v_" + encodeName(name)
		if m.Format == 3 {
			path = "$.__vs_vectors." + alias
		}
		fields = append(fields, nativeField{Name: path, Alias: alias, Kind: "VECTOR", Algorithm: strings.ToUpper(string(p.Index.Method)), Metric: metric, Dimensions: p.Dimensions, M: p.Index.HNSW.M, EFConstruction: p.Index.HNSW.EfConstruction})
	}
	if len(fields) > 512 {
		return nil, fmt.Errorf("%w: native Redis schema exceeds 512 fields", vectordata.ErrUnsupported)
	}
	return fields, nil
}

// Check both readiness and physical schema in the same Redis execution as a
// query. Lua uses RESP2 for stable module reply shapes, independent of the client.
const nativeReadyLua = `
redis.setresp(2)
local function text(v) if type(v)=='table' and v.ok then return v.ok end return v end
local function val(t,k)
 if type(t)~='table' then return nil end
 if t.map then t=t.map end
 if t[k]~=nil then return text(t[k]) end
 for i=1,#t-1 do local key=text(t[i]);if type(key)=='string' and string.lower(key)==string.lower(k) then return text(t[i+1]) end end
 return nil
end
local function flag(t,k)
 local flags=val(t,'flags') or t
 for _,v in ipairs(flags) do v=text(v);if type(v)=='string' and string.upper(v)==k then return true end end
 return false
end
local function ready(index,prefix,schema,count,keytype)
 local info=redis.pcall('FT.INFO',index)
 if info.err then
   if string.find(string.lower(info.err),'unknown index',1,true) or string.find(string.lower(info.err),'no such index',1,true) then return 2 end
   error(info.err)
 end
 local definition=val(info,'index_definition')
 local prefixes=val(definition,'prefixes')
 if val(definition,'key_type')~=(keytype or 'HASH') or type(prefixes)~='table' or #prefixes~=1 or text(prefixes[1])~=prefix then return -2 end
 local expected=cjson.decode(schema)
 local attrs=val(info,'attributes')
 if type(attrs)~='table' or #attrs~=#expected then return -2 end
 local hastext=false
 for _,f in ipairs(expected) do
   local a=nil
   for _,candidate in ipairs(attrs) do if val(candidate,'attribute')==f.Alias then a=candidate break end end
   if not a or val(a,'identifier')~=f.Name or val(a,'type')~=f.Kind then return -2 end
   if flag(a,'NOINDEX') then return -2 end
   if f.Kind=='TAG' and (not flag(a,'CASESENSITIVE') or ((keytype or 'HASH')=='HASH' and val(a,'SEPARATOR')~=',')) then return -2 end
   if f.Kind=='TEXT' and (tonumber(val(a,'WEIGHT'))~=1 or flag(a,'NOSTEM')) then return -2 end
   if f.Kind=='TEXT' then hastext=true end
   if f.Kind=='VECTOR' then
     if val(a,'algorithm')~=f.Algorithm or val(a,'data_type')~='FLOAT32' or tonumber(val(a,'dim'))~=f.Dimensions or val(a,'distance_metric')~=f.Metric then return -2 end
     if f.Algorithm=='HNSW' and (tonumber(val(a,'M'))~=f.M or tonumber(val(a,'ef_construction'))~=f.EFConstruction) then return -2 end
   end
 end
 if hastext then
   local options=val(info,'index_options') or {}
   if flag(options,'NOFIELDS') or flag(options,'NOOFFSETS') or flag(options,'NOFREQS') or val(info,'stopwords_list')~=nil then return -2 end
 end
 local failures=tonumber(val(info,'hash_indexing_failures'))
 if not failures then failures=tonumber(val(val(info,'Index Errors'),'indexing failures')) end
 if failures==nil or failures~=0 then return -3 end
 if tonumber(val(info,'indexing'))~=0 or tonumber(val(info,'num_docs'))~=tonumber(count) then return 2 end
 return 1
end
`

const nativeIndexStatus = nativeReadyLua + `return {ready(KEYS[1],ARGV[1],ARGV[2],ARGV[3],ARGV[4])}`

func (c *Collection) createNativeIndex(ctx context.Context, m hashManifest) error {
	fields, err := c.nativeFields(m)
	if err != nil {
		return err
	}
	keytype := "HASH"
	if m.Format == 3 {
		keytype = "JSON"
	}
	args := []any{"FT.CREATE", m.Index, "ON", keytype, "PREFIX", 1, c.nativeGenerationPrefix(m)}
	if m.IndexVersion == 2 {
		args = append(args, "LANGUAGE", "english")
	}
	args = append(args, "SCHEMA")
	for _, f := range fields {
		args = append(args, f.Name, "AS", f.Alias, f.Kind)
		if f.Kind == "TAG" {
			args = append(args, "CASESENSITIVE")
		} else if f.Kind == "TEXT" {
			args = append(args, "WEIGHT", 1)
		} else if f.Kind == "VECTOR" {
			attributes := []any{"TYPE", "FLOAT32", "DIM", f.Dimensions, "DISTANCE_METRIC", f.Metric}
			if f.Algorithm == "HNSW" {
				attributes = append(attributes, "M", f.M, "EF_CONSTRUCTION", f.EFConstruction)
			}
			args = append(args, f.Algorithm, len(attributes))
			args = append(args, attributes...)
		}
	}
	if err = c.store.client.Do(ctx, args...).Err(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unknown command") {
			return fmt.Errorf("%w: Redis Search is required: %w", vectordata.ErrUnsupported, err)
		}
		return err
	}
	schema, _ := json.Marshal(fields)
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		reply, err := c.store.client.Eval(wait, nativeIndexStatus, []string{m.Index}, c.nativeGenerationPrefix(m), string(schema), len(m.Tokens), keytype).Slice()
		if err != nil {
			return err
		}
		status, err := nativeStatus(reply)
		if err != nil {
			return err
		}
		if status == 1 {
			return nil
		}
		if status < 0 {
			return nativeStatusError(status)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
			return wait.Err()
		case <-timer.C:
		}
	}
}
func nativeStatus(result []any) (int64, error) {
	if len(result) == 0 {
		return 0, vectordata.ErrInvalidRecord
	}
	n, ok := result[0].(int64)
	if !ok {
		return 0, vectordata.ErrInvalidRecord
	}
	return n, nil
}
func nativeStatusError(status int64) error {
	if status == -2 {
		return fmt.Errorf("%w: native Redis index schema differs", vectordata.ErrSchemaMismatch)
	}
	return fmt.Errorf("%w: native Redis index/generation is damaged (%d)", vectordata.ErrInvalidRecord, status)
}
func (c *Collection) dropNativeIndex(ctx context.Context, name string) {
	if c.store.metadata.DatabaseName != "0" {
		return
	}
	for _, prefix := range []string{c.store.indexPrefix(c.name), c.store.jsonIndexPrefix(c.name)} {
		if strings.HasPrefix(name, prefix) && validRevision(strings.TrimPrefix(name, prefix)) {
			_ = c.store.client.Do(ctx, "FT.DROPINDEX", name).Err()
			return
		}
	}
}

// RebuildNativeIndexes explicitly publishes a fresh indexed hash/JSON generation.
// The model definition is unchanged; existing format-1 data must be upgraded first.
func (c *Collection) RebuildNativeIndexes(ctx context.Context) (err error) {
	defer func() { err = c.operation(ctx, "rebuild_native_indexes", err) }()
	if !c.store.nativeIndexes {
		return vectordata.ErrUnsupported
	}
	return c.mutate(ctx, func(state *snapshot) error {
		if state.Format != 2 && state.Format != 3 {
			return fmt.Errorf("%w: native index rebuild requires hash or JSON storage", vectordata.ErrUnsupported)
		}
		return nil
	})
}

const nativePruneGuard = `
local current=redis.call('GET',KEYS[1])
if ARGV[1]=='missing' then if current then return 0 end
elseif current~=ARGV[2] then return 0 end
if redis.call('PTTL',KEYS[2])>=0 then return 0 end
return 1`

// PruneNativeIndexes drops obsolete generation indexes without deleting records.
// Current indexes and active staging leases are protected. The result counts
// indexes, separately from PruneHashStorage's key count.
func (s *Store) PruneNativeIndexes(ctx context.Context, name string) (count int64, err error) {
	defer func() { err = s.operation(name, "prune_native_indexes", resolveContext(ctx, err)) }()
	if s.metadata.DatabaseName != "0" {
		return 0, vectordata.ErrUnsupported
	}
	if !validName(name) {
		return 0, vectordata.ErrSchemaMismatch
	}
	names, err := s.client.Do(ctx, "FT._LIST").StringSlice()
	if err != nil {
		return 0, err
	}
	for _, index := range names {
		prefix, storagePrefix := s.indexPrefix(name), s.hashPrefix(name)
		if strings.HasPrefix(index, s.jsonIndexPrefix(name)) {
			prefix, storagePrefix = s.jsonIndexPrefix(name), s.jsonPrefix(name)
		}
		revision := strings.TrimPrefix(index, prefix)
		if revision == index || !validRevision(revision) {
			continue
		}
		raw, getErr := s.client.Get(ctx, s.key(name)).Bytes()
		mode := "present"
		var current struct {
			Format   int
			Revision string
		}
		if errors.Is(getErr, goredis.Nil) {
			mode = "missing"
		} else if getErr != nil {
			return count, getErr
		} else if json.Unmarshal(raw, &current) != nil || (current.Format != 1 && current.Format != 2 && current.Format != 3) || !validRevision(current.Revision) {
			return count, vectordata.ErrInvalidRecord
		}
		if current.Revision == revision {
			continue
		}
		lease := storagePrefix + revision + ":lease"
		ok, guardErr := s.client.Eval(ctx, nativePruneGuard, []string{s.key(name), lease}, mode, raw).Int()
		if guardErr != nil {
			return count, guardErr
		}
		if ok == 0 {
			continue
		}
		if dropErr := s.client.Do(ctx, "FT.DROPINDEX", index).Err(); dropErr != nil {
			return count, dropErr
		}
		count++
	}
	return count, nil
}
