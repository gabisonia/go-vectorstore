package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

// All records, schema, sequence and revision live in one Redis string. Publishing
// one SET is the only mutation in CAS: Lua errors cannot leave a partial batch.
// Lua never parses JSON numbers (notably full-range integer keys and sequences).
const compareSwap = `
local current=redis.call('GET',KEYS[1])
if ARGV[1]=='create' then
  if current then return 0 end
elseif not current or current~=ARGV[2] then
  return 0
end
redis.call('SET',KEYS[1],ARGV[3])
return 1`

type snapshot struct {
	Format     int
	Definition string
	Revision   string
	Next       int64
	Records    map[string]json.RawMessage
}

type Collection struct {
	store        *Store
	name         string
	definition   vectordata.CollectionDefinition
	registration string
	options      vectordata.ModelCollectionOptions
}

func (c *Collection) Name() string                                { return c.name }
func (c *Collection) Definition() vectordata.CollectionDefinition { return c.definition.Clone() }
func (c *Collection) CollectionMetadata() vectordata.CollectionMetadata {
	return vectordata.CollectionMetadata{StoreMetadata: c.store.StoreMetadata(), CollectionName: c.name}
}
func (c *Collection) Exists(ctx context.Context) (bool, error) {
	return c.store.CollectionExists(ctx, c.name)
}
func (c *Collection) EnsureDeleted(ctx context.Context) error {
	return c.store.DeleteCollection(ctx, c.name)
}
func (c *Collection) operation(ctx context.Context, op string, err error) error {
	return c.store.operation(c.name, op, resolveContext(ctx, err))
}

func (c *Collection) load(ctx context.Context) (snapshot, []byte, error) {
	if err := ctx.Err(); err != nil {
		return snapshot{}, nil, err
	}
	raw, err := c.store.client.Get(ctx, c.store.key(c.name)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return snapshot{}, nil, vectordata.ErrCollectionNotFound
	}
	if err != nil {
		return snapshot{}, nil, err
	}
	var state snapshot
	if len(raw) > c.store.maxBytes {
		return state, nil, fmt.Errorf("%w: Redis collection exceeds snapshot byte limit", vectordata.ErrInvalidRecord)
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		return state, nil, fmt.Errorf("%w: malformed Redis collection: %v", vectordata.ErrInvalidRecord, err)
	}
	if state.Format != 1 || state.Definition != c.registration {
		return state, nil, vectordata.ErrSchemaMismatch
	}
	if state.Revision == "" || state.Next < 0 || state.Records == nil {
		return state, nil, fmt.Errorf("%w: invalid Redis snapshot", vectordata.ErrInvalidRecord)
	}
	return state, raw, nil
}
func (c *Collection) encode(state snapshot) ([]byte, error) {
	var err error
	state.Revision, err = vectordata.NewUUIDKey()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	if len(raw) > c.store.maxBytes {
		return nil, fmt.Errorf("%w: Redis collection exceeds snapshot byte limit", vectordata.ErrInvalidRecord)
	}
	return raw, nil
}
func (c *Collection) cas(ctx context.Context, mode string, old, new []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	n, err := c.store.client.Eval(ctx, compareSwap, []string{c.store.key(c.name)}, mode, old, new).Int()
	return n == 1, err
}
func (c *Collection) EnsureExists(ctx context.Context) (err error) {
	defer func() { err = c.operation(ctx, "ensure_exists", err) }()
	_, _, err = c.load(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, vectordata.ErrCollectionNotFound) {
		return err
	}
	raw, err := c.encode(snapshot{Format: 1, Definition: c.registration, Next: 1, Records: map[string]json.RawMessage{}})
	if err != nil {
		return err
	}
	ok, err := c.cas(ctx, "create", nil, raw)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	_, _, err = c.load(ctx)
	return err
}

// Retry only a negative CAS reply, which proves that this attempt wrote nothing.
// Network/timeout/Redis errors are returned immediately, with no uncertain replay.
func (c *Collection) mutate(ctx context.Context, apply func(*snapshot) error) error {
	for attempt := 0; attempt < 64; attempt++ {
		state, old, err := c.load(ctx)
		if err != nil {
			return err
		}
		if err = apply(&state); err != nil {
			return err
		}
		raw, err := c.encode(state)
		if err != nil {
			return err
		}
		ok, err := c.cas(ctx, "replace", old, raw)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		timer := time.NewTimer(time.Duration(min(attempt+1, 10)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("%w: Redis write contention exceeded 64 attempts", vectordata.ErrInvalidRecord)
}
func token(key any) string { raw, _ := json.Marshal(key); return string(raw) }
func (c *Collection) normalizeKeys(keys []any) ([]string, error) {
	out := make([]string, len(keys))
	for i, key := range keys {
		v, err := c.definition.NormalizeKey(key)
		if err != nil {
			return nil, err
		}
		if !modelcodec.ValidText(v) {
			return nil, fmt.Errorf("%w: keys require UTF-8", vectordata.ErrInvalidRecord)
		}
		out[i] = token(v)
	}
	return out, nil
}
func (c *Collection) Insert(ctx context.Context, records []vectordata.ModelRecord) (keys []any, err error) {
	defer func() { err = c.operation(ctx, "insert", err) }()
	return c.write(ctx, records, false)
}
func (c *Collection) Upsert(ctx context.Context, records []vectordata.ModelRecord) (keys []any, err error) {
	defer func() { err = c.operation(ctx, "upsert", err) }()
	return c.write(ctx, records, true)
}
func (c *Collection) write(ctx context.Context, records []vectordata.ModelRecord, upsert bool) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Check the registration before invoking an application embedder. Recheck in
	// every CAS attempt, since another client can delete/recreate the collection.
	if len(records) > 0 {
		if _, _, err := c.load(ctx); err != nil {
			return nil, err
		}
	}
	prepared, err := vectordata.PrepareModelRecords(ctx, c.definition, c.options, records)
	if err != nil {
		return nil, err
	}
	if len(prepared) == 0 {
		return []any{}, nil
	}
	// Allocate UUIDs and serialize before retrying, so callbacks/randomness are not
	// replayed. Integer sequences depend on the latest successful snapshot.
	payloads := make([]json.RawMessage, len(prepared))
	generated := make([]bool, len(prepared))
	for i := range prepared {
		generated[i] = prepared[i].Key == nil
		if generated[i] && c.definition.Key.Type == vectordata.KeyUUID {
			prepared[i].Key, err = vectordata.NewUUIDKey()
			if err != nil {
				return nil, err
			}
		}
		payloads[i], err = modelcodec.Encode(prepared[i])
		if err != nil {
			return nil, err
		}
	}
	keys := make([]any, len(prepared))
	err = c.mutate(ctx, func(state *snapshot) error {
		for i, r := range prepared {
			if err := ctx.Err(); err != nil {
				return err
			}
			payload := payloads[i]
			if generated[i] && c.definition.Key.Type == vectordata.KeyInt64 {
				if state.Next <= 0 {
					return fmt.Errorf("%w: generated key sequence exhausted", vectordata.ErrInvalidRecord)
				}
				r.Key = state.Next
				if state.Next == math.MaxInt64 {
					state.Next = 0
				} else {
					state.Next++
				}
				var err error
				payload, err = modelcodec.Encode(r)
				if err != nil {
					return err
				}
			}
			t := token(r.Key)
			if _, exists := state.Records[t]; exists && (!upsert || generated[i]) {
				return fmt.Errorf("%w: duplicate key", vectordata.ErrInvalidRecord)
			}
			state.Records[t] = payload
			keys[i] = r.Key
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}
func (c *Collection) Delete(ctx context.Context, keys []any) (count int64, err error) {
	defer func() { err = c.operation(ctx, "delete", err) }()
	tokens, err := c.normalizeKeys(keys)
	if err != nil {
		return 0, err
	}
	if len(tokens) == 0 {
		return 0, ctx.Err()
	}
	err = c.mutate(ctx, func(state *snapshot) error {
		count = 0
		for _, key := range tokens {
			if _, ok := state.Records[key]; ok {
				delete(state.Records, key)
				count++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
