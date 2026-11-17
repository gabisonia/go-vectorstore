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
	"strings"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

// StorageMode selects the physical layout for newly created collections.
type StorageMode string

const (
	StorageSnapshot StorageMode = "snapshot"
	StorageHash     StorageMode = "hash"
	StorageJSON     StorageMode = "json"
	stagingTTL                  = time.Hour
	hashBatchSize               = 128
)

var errManifestChanged = errors.New("Redis manifest changed")
var errLayoutUnchanged = errors.New("Redis storage layout unchanged")

// Format 2 keeps an atomic manifest at the existing collection key. Referenced
// hashes are immutable, generation-qualified, and have no TTL after publication.
type hashManifest struct {
	Format       int
	Definition   string
	Revision     string
	Next         int64
	Tokens       []string
	Index        string   `json:",omitempty"`
	IndexVersion int      `json:",omitempty"`
	IndexVectors []string `json:",omitempty"`
}

type hashRecord struct {
	key    string
	fields []any
}

// A generation lease prevents accidental reuse of any existing generation.
// Stage errors can leave auxiliary keys, but never publish a partial collection.
const stageHash = `
if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end
if redis.call('EXISTS',KEYS[2])~=0 then return 0 end
for i=3,#ARGV,256 do
  redis.call('HSET',KEYS[2],unpack(ARGV,i,math.min(i+255,#ARGV)))
end
redis.call('PEXPIRE',KEYS[2],ARGV[2])
return 1`

// Only the final SET publishes logical changes. Earlier PERSIST operations touch
// unpublished staging keys. Errors leave the previous manifest/sequence intact.
const publishHash = `
local current=redis.call('GET',KEYS[1])
if ARGV[1]=='create' then
  if current then return 0 end
elseif not current or current~=ARGV[2] then return 0 end
if redis.call('GET',KEYS[2])~=ARGV[4] then return -1 end
for i=3,#KEYS do
  if redis.call('HGET',KEYS[i],'__vs_revision')~=ARGV[4] or
     not redis.call('HGET',KEYS[i],'__vs_payload') then return -1 end
end
for i=2,#KEYS do
  redis.call('PERSIST',KEYS[i])
  if redis.call('EXISTS',KEYS[i])~=1 then return -1 end
end
redis.call('SET',KEYS[1],ARGV[3])
return 1`

// No writes occur while reading. The manifest check and all payload reads share
// one Redis execution, so retiring an older generation cannot race this read.
const readHash = `
if redis.call('GET',KEYS[1])~=ARGV[1] then return {0} end
if redis.call('GET',KEYS[2])~=ARGV[2] then return {-1} end
local records={}
for i=3,#KEYS do
  local row=redis.call('HMGET',KEYS[i],'__vs_revision','__vs_token','__vs_payload')
  if row[1]~=ARGV[2] or row[2]~=ARGV[i] or not row[3] then return {-1} end
  records[#records+1]=row[3]
end
return {1,records}`

// Current manifest equality protects a concurrent publication/recreation. Keys
// with TTLs belong to active staging and are left for publication or expiry.
const pruneHashes = `
local current=redis.call('GET',KEYS[1])
if ARGV[1]=='missing' then
  if current then return -1 end
elseif current~=ARGV[2] then return -1 end
local obsolete={}
for i=2,#KEYS do
  if redis.call('PTTL',KEYS[i])==-1 then obsolete[#obsolete+1]=KEYS[i] end
end
if #obsolete==0 then return 0 end
return redis.call('UNLINK',unpack(obsolete))`

func (s *Store) hashPrefix(name string) string {
	return "__vectorstore_redis_hash_v2:" + encodeName(s.metadata.Namespace) + ":" + encodeName(name) + ":"
}
func (c *Collection) generationPrefix(revision string) string {
	return c.store.hashPrefix(c.name) + revision + ":"
}
func (c *Collection) hashKeys(m hashManifest) []string {
	prefix := c.generationPrefix(m.Revision)
	keys := []string{c.store.key(c.name), prefix + "lease"}
	for _, t := range m.Tokens {
		keys = append(keys, prefix+"r:"+encodeName(t))
	}
	return keys
}
func validRevision(revision string) bool {
	if len(revision) != 36 {
		return false
	}
	for i, b := range []byte(revision) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if b != '-' {
				return false
			}
		} else if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

func (c *Collection) loadHashes(ctx context.Context, raw []byte) (snapshot, []byte, error) {
	var m hashManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return snapshot{}, nil, err
	}
	if m.Format != 2 || m.Definition != c.registration {
		return snapshot{}, nil, vectordata.ErrSchemaMismatch
	}
	if !validRevision(m.Revision) || m.Next < 0 || m.Tokens == nil {
		return snapshot{}, nil, fmt.Errorf("%w: malformed Redis hash manifest", vectordata.ErrInvalidRecord)
	}
	seen := map[string]bool{}
	args := []any{raw, m.Revision}
	for _, t := range m.Tokens {
		if t == "" || seen[t] {
			return snapshot{}, nil, fmt.Errorf("%w: duplicate/empty Redis manifest token", vectordata.ErrInvalidRecord)
		}
		seen[t] = true
		args = append(args, t)
	}
	keys := c.hashKeys(m)
	result, err := c.store.client.Eval(ctx, readHash, keys, args...).Slice()
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
		return snapshot{}, nil, fmt.Errorf("%w: missing/damaged Redis hash generation", vectordata.ErrInvalidRecord)
	}
	payloads, ok := result[1].([]any)
	if !ok || len(payloads) != len(m.Tokens) {
		return snapshot{}, nil, vectordata.ErrInvalidRecord
	}
	state := snapshot{Format: 2, Definition: m.Definition, Revision: m.Revision, Next: m.Next, Records: map[string]json.RawMessage{}}
	size := len(raw)
	for i, value := range payloads {
		if err := ctx.Err(); err != nil {
			return snapshot{}, nil, err
		}
		payload, ok := value.(string)
		if !ok {
			return snapshot{}, nil, vectordata.ErrInvalidRecord
		}
		size += len(keys[i+2]) + len(payload)
		if size > c.store.maxBytes {
			return snapshot{}, nil, fmt.Errorf("%w: Redis hash generation exceeds byte limit", vectordata.ErrInvalidRecord)
		}
		state.Records[m.Tokens[i]] = json.RawMessage(payload)
	}
	return state, raw, nil
}

// projectHashes keeps a canonical payload alongside native scalar/vector fields.
// Raw int64 projections remain decimal strings; native indexing adds separate
// high/low 32-bit NUMERIC fields to preserve full-range integer ordering.
func (c *Collection) projectHashes(ctx context.Context, state snapshot) (hashManifest, []byte, []hashRecord, error) {
	m := hashManifest{Format: 2, Definition: state.Definition, Revision: state.Revision, Next: state.Next, Tokens: make([]string, 0, len(state.Records))}
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
		m.Index = c.indexName(m.Revision)
		m.IndexVersion = c.nativeIndexVersion()
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
				m.IndexVectors = append(m.IndexVectors, p.Name)
			}
		}
	}
	sort.Strings(m.Tokens)
	raw, err := json.Marshal(m)
	if err != nil {
		return m, nil, nil, err
	}
	keys := c.hashKeys(m)
	size := len(raw) + len(keys[1]) + len(m.Revision)
	rows := make([]hashRecord, 0, len(records))
	for i, t := range m.Tokens {
		if err := ctx.Err(); err != nil {
			return m, nil, nil, err
		}
		r := byToken[t]
		fields := []any{"__vs_payload", string(state.Records[t]), "__vs_revision", m.Revision, "__vs_token", t}
		for _, p := range c.definition.Data {
			v := r.Data[p.Name]
			if v == nil {
				continue
			}
			var text string
			switch p.Type {
			case vectordata.TypeString:
				text = v.(string)
			case vectordata.TypeInt64:
				text = strconv.FormatInt(v.(int64), 10)
			case vectordata.TypeFloat64:
				text = strconv.FormatFloat(v.(float64), 'g', -1, 64)
			case vectordata.TypeBool:
				if v.(bool) {
					text = "1"
				} else {
					text = "0"
				}
			default:
				continue
			}
			fields = append(fields, "d_"+encodeName(p.Name), text)
		}
		for _, p := range c.definition.Vectors {
			vector := r.Vectors[p.Name]
			if vector == nil {
				continue
			}
			blob := make([]byte, 4*len(vector))
			for j, v := range vector {
				binary.LittleEndian.PutUint32(blob[j*4:], math.Float32bits(v))
			}
			fields = append(fields, "v_"+encodeName(p.Name), string(blob))
		}
		if c.store.nativeIndexes {
			fields = append(fields, c.nativeProjection(r)...)
		}
		size += len(keys[i+2])
		for _, field := range fields {
			size += len(field.(string))
		}
		if size > c.store.maxBytes {
			return m, nil, nil, fmt.Errorf("%w: Redis hash generation exceeds byte limit", vectordata.ErrInvalidRecord)
		}
		rows = append(rows, hashRecord{key: keys[i+2], fields: fields})
	}
	if size > c.store.maxBytes {
		return m, nil, nil, fmt.Errorf("%w: Redis hash generation exceeds byte limit", vectordata.ErrInvalidRecord)
	}
	return m, raw, rows, nil
}

func (c *Collection) unlinkGeneration(ctx context.Context, keys []string) {
	// The caller has proved this generation is unpublished or already retired.
	// Never unlink the new generation after an uncertain publication result.
	for start := 0; start < len(keys); start += hashBatchSize {
		end := min(start+hashBatchSize, len(keys))
		if err := c.store.client.Unlink(ctx, keys[start:end]...).Err(); err != nil {
			return
		}
	}
}

func (c *Collection) publishHashes(ctx context.Context, mode string, old []byte, state snapshot) (bool, error) {
	m, raw, rows, err := c.projectHashes(ctx, state)
	if err != nil {
		return false, err
	}
	keys := c.hashKeys(m)
	claimed, err := c.store.client.SetNX(ctx, keys[1], m.Revision, stagingTTL).Result()
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, fmt.Errorf("%w: Redis hash generation already exists", vectordata.ErrInvalidRecord)
	}
	for start := 0; start < len(rows); start += hashBatchSize {
		var cmds []*goredis.Cmd
		_, err = c.store.client.Pipelined(ctx, func(pipe goredis.Pipeliner) error {
			for _, row := range rows[start:min(start+hashBatchSize, len(rows))] {
				args := []any{m.Revision, stagingTTL.Milliseconds()}
				args = append(args, row.fields...)
				cmds = append(cmds, pipe.Eval(ctx, stageHash, []string{keys[1], row.key}, args...))
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
					err = fmt.Errorf("%w: Redis hash staging expired/collided", vectordata.ErrInvalidRecord)
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
	n, err := c.store.client.Eval(ctx, publishHash, keys, mode, old, raw, m.Revision).Int()
	if err != nil {
		return false, err
	}
	if n == 0 {
		c.dropNativeIndex(ctx, m.Index)
		c.unlinkGeneration(ctx, keys[1:])
		return false, nil
	}
	if n != 1 {
		return false, fmt.Errorf("%w: incomplete Redis hash staging", vectordata.ErrInvalidRecord)
	}
	// The old generation cannot be republished: revisions are uniquely leased.
	// An old reader either completed atomically or will retry its manifest check.
	var previous hashManifest
	if json.Unmarshal(old, &previous) == nil && previous.Format == 2 {
		oldKeys := c.hashKeys(previous)
		c.unlinkGeneration(ctx, oldKeys[1:])
		c.dropNativeIndex(ctx, previous.Index)
	}
	return true, nil
}

// MigrateToHashStorage explicitly upgrades a snapshot collection to format 2.
// Schema, records and generated sequence publish together. Readers of this
// provider support both layouts; older binaries reject format 2. NativeIndexes
// also builds indexes during conversion. This is separate from schema migration.
func (c *Collection) MigrateToHashStorage(ctx context.Context) (err error) {
	defer func() { err = c.operation(ctx, "migrate_hash_storage", err) }()
	return c.mutate(ctx, func(state *snapshot) error {
		if state.Format == 2 {
			return errLayoutUnchanged
		}
		if state.Format != 1 {
			return fmt.Errorf("%w: only snapshot-to-hash conversion is supported", vectordata.ErrUnsupported)
		}
		state.Format = 2
		return nil
	})
}

// PruneHashStorage removes obsolete non-expiring auxiliary keys for a collection,
// even after deletion. Current generations and active TTL staging are protected.
// The count is Redis keys removed, not records. Concurrent changes can leave keys
// for a later pass. Logical collection data is never changed by pruning.
func (s *Store) PruneHashStorage(ctx context.Context, name string) (count int64, err error) {
	defer func() { err = s.operation(name, "prune_hash_storage", resolveContext(ctx, err)) }()
	return s.pruneStorage(ctx, name, s.hashPrefix(name), 2)
}

func (s *Store) pruneStorage(ctx context.Context, name, prefix string, format int) (count int64, err error) {
	if !validName(name) {
		return 0, vectordata.ErrSchemaMismatch
	}
	var cursor uint64
	for {
		if err = ctx.Err(); err != nil {
			return count, err
		}
		keys, next, scanErr := s.client.Scan(ctx, cursor, prefix+"*", hashBatchSize).Result()
		if scanErr != nil {
			return count, scanErr
		}
		for start := 0; start < len(keys); start += hashBatchSize {
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
				return count, fmt.Errorf("%w: cannot prune with unknown Redis manifest", vectordata.ErrInvalidRecord)
			}
			candidates := []string{s.key(name)}
			for _, key := range keys[start:min(start+hashBatchSize, len(keys))] {
				suffix := strings.TrimPrefix(key, prefix)
				revision, _, ok := strings.Cut(suffix, ":")
				if ok && validRevision(revision) && (current.Format != format || revision != current.Revision) {
					candidates = append(candidates, key)
				}
			}
			if len(candidates) > 1 {
				n, evalErr := s.client.Eval(ctx, pruneHashes, candidates, mode, raw).Int64()
				if evalErr != nil {
					return count, evalErr
				}
				if n > 0 {
					count += n
				}
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return count, nil
}
