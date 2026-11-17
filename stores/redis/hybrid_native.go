package redis

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Both searches and the union's canonical records belong to the verified
// generation. Scan mode reads all manifest members for exact vector/filter
// fallback. Only outer JSON wrappers are parsed; canonical numbers stay opaque.
const nativeHybridExecute = nativeReadyLua + `
if redis.call('GET',KEYS[1])~=ARGV[1] then return {0} end
if redis.call('GET',KEYS[2])~=ARGV[2] then return {-1} end
local status=ready(KEYS[3],ARGV[3],ARGV[4],ARGV[5],ARGV[6])
if status~=1 then return {status} end
local nv=tonumber(ARGV[7])
local vectors={0}
if nv>0 then vectors=redis.call('FT.SEARCH',KEYS[3],unpack(ARGV,8,7+nv)) end
local keywords=redis.call('FT.SEARCH',KEYS[3],unpack(ARGV,8+nv))
local payloads={0}
local seen={}
local function fetch(key)
 if seen[key] then return true end
 local payload,token,revision
 if ARGV[6]=='JSON' then
  local raw=redis.call('JSON.GET',key)
  if not raw then return false end
  local ok,row=pcall(cjson.decode,raw)
  if not ok or type(row)~='table' then return false end
  payload,token,revision=row.__vs_payload,row.__vs_token,row.__vs_revision
 else
  local row=redis.call('HMGET',key,'__vs_payload','__vs_token','__vs_revision')
  payload,token,revision=row[1],row[2],row[3]
 end
 if type(payload)~='string' or payload=='' or type(token)~='string' or revision~=ARGV[2] then return false end
 seen[key]=true
 payloads[1]=payloads[1]+1
 payloads[#payloads+1]=key
 payloads[#payloads+1]={'__vs_payload',payload,'__vs_token',token,'__vs_revision',revision}
 return true
end
if nv==0 then
 for i=4,#KEYS do
  if not fetch(KEYS[i]) then return {-1} end
  vectors[#vectors+1]=KEYS[i]
  vectors[1]=vectors[1]+1
 end
else
 for i=2,#vectors do if not fetch(text(vectors[i])) then return {-1} end end
end
for i=2,#keywords,2 do if not fetch(text(keywords[i])) then return {-1} end end
return {1,vectors,keywords,payloads}`

func hybridSearchArgs(query string, params []any, limit int, scored bool) []any {
	args := []any{query, "NOCONTENT"}
	if scored {
		args = append(args, "WITHSCORES", "SCORER", "BM25")
	}
	if len(params) > 0 {
		args = append(args, "PARAMS", len(params))
		args = append(args, params...)
	}
	if !scored {
		args = append(args, "SORTBY", "__vs_distance", "ASC")
	}
	return append(args, "LIMIT", 0, limit, "DIALECT", 2, "TIMEOUT", 0)
}
func (c *Collection) hybridRead(ctx context.Context, m hashManifest, raw []byte, cfg hybridConfig, vector []float32, scan bool, k int) ([]vectordata.ModelRecord, []string, map[string]float64, error) {
	keys, args := c.hybridHeaderArgs(m, raw)
	plan, supported, err := c.compileNativeFilter(cfg.opts.Filter)
	if err != nil {
		return nil, nil, nil, err
	}
	if !supported {
		plan = nativeFilterPlan{query: nativeAll}
	}
	var vectorArgs []any
	if scan {
		if m.Format == 3 {
			keys = append(keys, c.jsonKeys(m)[2:]...)
		} else {
			keys = append(keys, c.hashKeys(m)[2:]...)
		}
	} else {
		blob := make([]byte, 4*len(vector))
		for i, x := range vector {
			binary.LittleEndian.PutUint32(blob[4*i:], math.Float32bits(x))
		}
		params := append([]any(nil), plan.params...)
		params = append(params, "__k", strconv.Itoa(k), "__q", string(blob))
		query := "(" + plan.query + ")=>[KNN $__k @" + cfg.vectorAlias + " $__q"
		if cfg.vector.Index.Method == vectordata.IndexMethodHNSW {
			ef := cfg.tuning.EFRuntime
			if ef == 0 {
				ef = 128
			}
			query += " EF_RUNTIME $__ef"
			params = append(params, "__ef", strconv.Itoa(ef))
		}
		vectorArgs = hybridSearchArgs(query+" AS __vs_distance]", params, k, false)
	}
	textQuery := cfg.keywordQuery
	if textQuery != nativeNone {
		textQuery = "@" + cfg.textAlias + ":(" + textQuery + ")"
	}
	textQuery = nativeJoin([]string{plan.query, textQuery}, true)
	textParams := append([]any(nil), plan.params...)
	textParams = append(textParams, cfg.keywordParams...)
	args = append(args, len(vectorArgs))
	args = append(args, vectorArgs...)
	args = append(args, hybridSearchArgs(textQuery, textParams, len(m.Tokens), true)...)
	result, err := c.store.client.Eval(ctx, nativeHybridExecute, keys, args...).Slice()
	if err != nil {
		return nil, nil, nil, err
	}
	if err = hybridStatus(result); err != nil {
		return nil, nil, nil, err
	}
	if len(result) != 4 {
		return nil, nil, nil, vectordata.ErrInvalidRecord
	}
	vr, vok := result[1].([]any)
	tr, tok := result[2].([]any)
	pr, pok := result[3].([]any)
	if !vok || !tok || !pok {
		return nil, nil, nil, vectordata.ErrInvalidRecord
	}
	total, err := nativeTotal(vr)
	if err != nil || total != len(vr)-1 {
		return nil, nil, nil, fmt.Errorf("%w: incomplete hybrid vector results", vectordata.ErrInvalidRecord)
	}
	ids := make([]string, 0, total)
	seen := map[string]bool{}
	for _, value := range vr[1:] {
		id, ok := value.(string)
		if !ok || seen[id] {
			return nil, nil, nil, vectordata.ErrInvalidRecord
		}
		seen[id] = true
		ids = append(ids, id)
	}
	total, err = nativeTotal(tr)
	if err != nil || len(tr) != 1+2*total {
		return nil, nil, nil, fmt.Errorf("%w: Redis result limit prevents complete hybrid keyword ranking", vectordata.ErrUnsupported)
	}
	scores := make(map[string]float64, total)
	for i := 1; i < len(tr); i += 2 {
		id, ok := tr[i].(string)
		value, sok := tr[i+1].(string)
		if !ok || !sok {
			return nil, nil, nil, vectordata.ErrInvalidRecord
		}
		if _, duplicate := scores[id]; duplicate {
			return nil, nil, nil, vectordata.ErrInvalidRecord
		}
		score, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, nil, nil, vectordata.ErrInvalidRecord
		}
		scores[id] = score
	}
	payloadCount, err := nativeTotal(pr)
	if err != nil || len(pr) != 1+2*payloadCount || payloadCount > len(m.Tokens) {
		return nil, nil, nil, vectordata.ErrInvalidRecord
	}
	rows, err := c.nativeRecords(ctx, m, pr)
	if err != nil {
		return nil, nil, nil, err
	}
	members := map[string]bool{}
	for _, row := range rows {
		members[c.nativeGenerationPrefix(m)+"r:"+encodeName(token(row.Key))] = true
	}
	for _, id := range ids {
		if !members[id] {
			return nil, nil, nil, vectordata.ErrInvalidRecord
		}
	}
	for id := range scores {
		if !members[id] {
			return nil, nil, nil, vectordata.ErrInvalidRecord
		}
	}
	return rows, ids, scores, nil
}
