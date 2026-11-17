package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

// Format 3 uses RedisJSON documents, with the same opaque canonical record
// encoding as other layouts. Only wrapper metadata is parsed in Lua/RedisJSON;
// canonical uint64, decimal and JSON numbers never enter their numeric parsers.
type jsonManifest = hashManifest

type jsonDocument struct {
	Revision string               `json:"__vs_revision"`
	Token    string               `json:"__vs_token"`
	Payload  string               `json:"__vs_payload"`
	Vectors  map[string][]float32 `json:"__vs_vectors,omitempty"`
	Scalars  map[string]any       `json:"__vs_scalars,omitempty"`
	Text     map[string]string    `json:"__vs_text,omitempty"`
}

type jsonRecord struct{ key, document string }

const stageJSON = `
if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end
if redis.call('EXISTS',KEYS[2])~=0 then return 0 end
redis.call('JSON.SET',KEYS[2],'$',ARGV[3])
redis.call('PEXPIRE',KEYS[2],ARGV[2])
return 1`

// Decode only the outer wrapper; __vs_payload is always an opaque string.
const jsonRecordLua = `
local function document(key,revision,token)
 local raw=redis.call('JSON.GET',key)
 if not raw then return nil end
 local ok,row=pcall(cjson.decode,raw)
 if not ok or type(row)~='table' or row.__vs_revision~=revision or row.__vs_token~=token or type(row.__vs_payload)~='string' or row.__vs_payload=='' then return nil end
 return raw
end
`

// PERSIST touches unpublished staging only. The final SET alone publishes data
// and generated sequence. Never replay an uncertain response from this script.
const publishJSON = jsonRecordLua + `
local current=redis.call('GET',KEYS[1])
if ARGV[1]=='create' then
 if current then return 0 end
elseif not current or current~=ARGV[2] then return 0 end
if redis.call('GET',KEYS[2])~=ARGV[4] then return -1 end
for i=3,#KEYS do
 if not document(KEYS[i],ARGV[4],ARGV[i+2]) then return -1 end
end
for i=2,#KEYS do
 redis.call('PERSIST',KEYS[i])
 if redis.call('EXISTS',KEYS[i])~=1 then return -1 end
end
redis.call('SET',KEYS[1],ARGV[3])
return 1`

const readJSON = jsonRecordLua + `
if redis.call('GET',KEYS[1])~=ARGV[1] then return {0} end
if redis.call('GET',KEYS[2])~=ARGV[2] then return {-1} end
local records={}
for i=3,#KEYS do
 local raw=document(KEYS[i],ARGV[2],ARGV[i])
 if not raw then return {-1} end
 records[#records+1]=raw
end
return {1,records}`

func (s *Store) jsonPrefix(name string) string {
	return "__vectorstore_redis_json_v3:" + encodeName(s.metadata.Namespace) + ":" + encodeName(name) + ":"
}
func (c *Collection) jsonGenerationPrefix(revision string) string {
	return c.store.jsonPrefix(c.name) + revision + ":"
}
func (c *Collection) jsonKeys(m jsonManifest) []string {
	prefix := c.jsonGenerationPrefix(m.Revision)
	keys := []string{c.store.key(c.name), prefix + "lease"}
	for _, t := range m.Tokens {
		keys = append(keys, prefix+"r:"+encodeName(t))
	}
	return keys
}

func (c *Collection) projectJSON(ctx context.Context, state snapshot) (jsonManifest, []byte, []jsonRecord, error) {
	m := jsonManifest{Format: 3, Definition: state.Definition, Revision: state.Revision, Next: state.Next, Tokens: make([]string, 0, len(state.Records))}
	records, err := c.decode(ctx, state, nil)
	if err != nil {
		return m, nil, nil, err
	}
	byToken := map[string]vectordata.ModelRecord{}
	for _, r := range records {
		t := token(r.Key)
		m.Tokens = append(m.Tokens, t)
		byToken[t] = r
	}
	if c.store.nativeIndexes {
		m.Index, m.IndexVersion = c.nativeIndexName(3, m.Revision), c.nativeJSONIndexVersion()
		m.IndexVectors = c.nativeVectors(records)
	}
	sort.Strings(m.Tokens)
	raw, err := json.Marshal(m)
	if err != nil {
		return m, nil, nil, err
	}
	keys := c.jsonKeys(m)
	size := len(raw) + len(keys[1]) + len(m.Revision)
	rows := make([]jsonRecord, 0, len(m.Tokens))
	for i, t := range m.Tokens {
		if err := ctx.Err(); err != nil {
			return m, nil, nil, err
		}
		doc := jsonDocument{Revision: m.Revision, Token: t, Payload: string(state.Records[t])}
		if c.store.nativeIndexes {
			doc.Scalars = c.jsonScalarProjection(byToken[t])
		}
		if m.IndexVersion == 5 {
			for i, p := range c.definition.Data {
				if nativeText(p) && byToken[t].Data[p.Name] != nil {
					if doc.Text == nil {
						doc.Text = map[string]string{}
					}
					doc.Text["text"+strconv.Itoa(i)] = byToken[t].Data[p.Name].(string)
				}
			}
		}
		for i, p := range c.definition.Vectors {
			for _, name := range m.IndexVectors {
				if name == p.Name && byToken[t].Vectors[name] != nil {
					if doc.Vectors == nil {
						doc.Vectors = map[string][]float32{}
					}
					doc.Vectors["v"+strconv.Itoa(i)] = byToken[t].Vectors[name]
				}
			}
		}
		encoded, err := json.Marshal(doc)
		if err != nil {
			return m, nil, nil, err
		}
		size += len(keys[i+2]) + len(encoded)
		if size > c.store.maxBytes {
			return m, nil, nil, fmt.Errorf("%w: RedisJSON generation exceeds byte limit", vectordata.ErrInvalidRecord)
		}
		rows = append(rows, jsonRecord{key: keys[i+2], document: string(encoded)})
	}
	if size > c.store.maxBytes {
		return m, nil, nil, fmt.Errorf("%w: RedisJSON generation exceeds byte limit", vectordata.ErrInvalidRecord)
	}
	return m, raw, rows, nil
}

func (c *Collection) loadJSON(ctx context.Context, raw []byte) (snapshot, []byte, error) {
	var m jsonManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return snapshot{}, nil, err
	}
	if m.Format != 3 || m.Definition != c.registration {
		return snapshot{}, nil, vectordata.ErrSchemaMismatch
	}
	if !validRevision(m.Revision) || m.Next < 0 || m.Tokens == nil {
		return snapshot{}, nil, fmt.Errorf("%w: malformed RedisJSON manifest", vectordata.ErrInvalidRecord)
	}
	seen := map[string]bool{}
	args := []any{raw, m.Revision}
	for _, t := range m.Tokens {
		if t == "" || seen[t] {
			return snapshot{}, nil, fmt.Errorf("%w: duplicate/empty RedisJSON manifest token", vectordata.ErrInvalidRecord)
		}
		seen[t] = true
		args = append(args, t)
	}
	keys := c.jsonKeys(m)
	result, err := c.store.client.Eval(ctx, readJSON, keys, args...).Slice()
	if err != nil {
		return snapshot{}, nil, err
	}
	if len(result) == 0 {
		return snapshot{}, nil, vectordata.ErrInvalidRecord
	}
	if result[0] == int64(0) {
		return snapshot{}, nil, errManifestChanged
	}
	if result[0] != int64(1) || len(result) != 2 {
		return snapshot{}, nil, fmt.Errorf("%w: missing/damaged RedisJSON generation", vectordata.ErrInvalidRecord)
	}
	documents, ok := result[1].([]any)
	if !ok || len(documents) != len(m.Tokens) {
		return snapshot{}, nil, vectordata.ErrInvalidRecord
	}
	state := snapshot{Format: 3, Definition: m.Definition, Revision: m.Revision, Next: m.Next, Records: map[string]json.RawMessage{}}
	size := len(raw) + len(keys[1]) + len(m.Revision)
	for i, value := range documents {
		if err := ctx.Err(); err != nil {
			return snapshot{}, nil, err
		}
		encoded, ok := value.(string)
		if !ok {
			return snapshot{}, nil, vectordata.ErrInvalidRecord
		}
		size += len(keys[i+2]) + len(encoded)
		if size > c.store.maxBytes {
			return snapshot{}, nil, fmt.Errorf("%w: RedisJSON generation exceeds byte limit", vectordata.ErrInvalidRecord)
		}
		var doc jsonDocument
		if err := json.Unmarshal([]byte(encoded), &doc); err != nil || doc.Revision != m.Revision || doc.Token != m.Tokens[i] || doc.Payload == "" {
			return snapshot{}, nil, fmt.Errorf("%w: damaged RedisJSON document", vectordata.ErrInvalidRecord)
		}
		state.Records[m.Tokens[i]] = json.RawMessage(doc.Payload)
	}
	return state, raw, nil
}

func (c *Collection) publishJSON(ctx context.Context, mode string, old []byte, state snapshot) (bool, error) {
	m, raw, rows, err := c.projectJSON(ctx, state)
	if err != nil {
		return false, err
	}
	// Even an empty generation requires RedisJSON. The unique, reserved probe key
	// is never written; no COMMAND permission or constructor network I/O is needed.
	if err := c.store.client.Do(ctx, "JSON.TYPE", c.jsonGenerationPrefix(m.Revision)+"probe").Err(); err != nil && !errors.Is(err, goredis.Nil) {
		if strings.Contains(strings.ToLower(err.Error()), "unknown command") {
			return false, fmt.Errorf("%w: RedisJSON is required: %w", vectordata.ErrUnsupported, err)
		}
		return false, err
	}
	keys := c.jsonKeys(m)
	claimed, err := c.store.client.SetNX(ctx, keys[1], m.Revision, stagingTTL).Result()
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, fmt.Errorf("%w: RedisJSON generation already exists", vectordata.ErrInvalidRecord)
	}
	for start := 0; start < len(rows); start += hashBatchSize {
		var cmds []*goredis.Cmd
		_, err = c.store.client.Pipelined(ctx, func(pipe goredis.Pipeliner) error {
			for _, row := range rows[start:min(start+hashBatchSize, len(rows))] {
				cmds = append(cmds, pipe.Eval(ctx, stageJSON, []string{keys[1], row.key}, m.Revision, stagingTTL.Milliseconds(), row.document))
			}
			return nil
		})
		if err == nil {
			for _, cmd := range cmds {
				n, cmdErr := cmd.Int()
				if cmdErr != nil {
					err = cmdErr
					break
				}
				if n != 1 {
					err = fmt.Errorf("%w: RedisJSON staging expired/collided", vectordata.ErrInvalidRecord)
					break
				}
			}
		}
		if err != nil {
			c.unlinkGeneration(ctx, keys[1:])
			return false, err
		}
	}
	if m.Index != "" {
		if err := c.createNativeIndex(ctx, m); err != nil {
			c.dropNativeIndex(ctx, m.Index)
			c.unlinkGeneration(ctx, keys[1:])
			return false, err
		}
	}
	args := []any{mode, old, raw, m.Revision}
	for _, t := range m.Tokens {
		args = append(args, t)
	}
	n, err := c.store.client.Eval(ctx, publishJSON, keys, args...).Int()
	if err != nil {
		return false, err
	}
	if n == 0 {
		c.dropNativeIndex(ctx, m.Index)
		c.unlinkGeneration(ctx, keys[1:])
		return false, nil
	}
	if n != 1 {
		return false, fmt.Errorf("%w: incomplete RedisJSON staging", vectordata.ErrInvalidRecord)
	}
	var previous jsonManifest
	if json.Unmarshal(old, &previous) == nil {
		switch previous.Format {
		case 2:
			c.unlinkGeneration(ctx, c.hashKeys(previous)[1:])
			c.dropNativeIndex(ctx, previous.Index)
		case 3:
			c.unlinkGeneration(ctx, c.jsonKeys(previous)[1:])
			c.dropNativeIndex(ctx, previous.Index)
		}
	}
	return true, nil
}

// MigrateToJSONStorage explicitly converts snapshots or hashes to format 3.
// Schema, canonical records and generated sequence publish atomically. Repeating
// it on JSON storage is a read-only no-op. Native mode builds scalar/vector indexes.
func (c *Collection) MigrateToJSONStorage(ctx context.Context) (err error) {
	defer func() { err = c.operation(ctx, "migrate_json_storage", err) }()
	return c.mutate(ctx, func(state *snapshot) error {
		if state.Format == 3 {
			return errLayoutUnchanged
		}
		state.Format = 3
		return nil
	})
}

// PruneJSONStorage removes obsolete non-expiring JSON documents and leases,
// including after deletion. Current generations and active TTL staging survive.
// The result counts Redis keys, not records; concurrent changes can leave work.
func (s *Store) PruneJSONStorage(ctx context.Context, name string) (count int64, err error) {
	defer func() { err = s.operation(name, "prune_json_storage", resolveContext(ctx, err)) }()
	return s.pruneStorage(ctx, name, s.jsonPrefix(name), 3)
}
