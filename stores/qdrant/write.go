package qdrant

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

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
	if len(records) > c.store.limits.MaxRecords {
		return nil, ErrLimit
	}
	// Check the registration before invoking an application embedder. Recheck in
	// every CAS attempt, since another client can delete/recreate the collection.
	var epoch string
	if len(records) > 0 {
		state, _, err := c.load(ctx)
		if err != nil {
			return nil, err
		}
		epoch = state.Epoch
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
	err = c.mutateEpoch(ctx, epoch, func(state *snapshot) error {
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
