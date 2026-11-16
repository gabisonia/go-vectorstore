package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Recognize only a complete, validated Eq/In predicate on the logical key.
// Compound/data/JSON predicates retain their existing evaluation path. Resolve
// caller-defined key values before acquiring SQLite's single connection.
func (c *Collection) keyFilterTokens(ctx context.Context, filter vectordata.Filter) ([]string, bool, error) {
	var field vectordata.FieldRef
	var values []any
	switch f := filter.(type) {
	case vectordata.EqFilter:
		field, values = f.Field, []any{f.Value}
	case vectordata.InFilter:
		field, values = f.Field, f.Values
	default:
		return nil, false, nil
	}
	field, err := vectordata.NormalizeFieldRef(field)
	if err != nil {
		return nil, false, err
	}
	if field.Kind != vectordata.FieldColumn || field.Name != c.definition.Key.Name {
		return nil, false, nil
	}
	tokens := make([]string, 0, min(len(values), lookupBatchSize))
	seen := make(map[string]struct{}, min(len(values), lookupBatchSize))
	for _, value := range values {
		if err := ctx.Err(); err != nil {
			return nil, true, err
		}
		// Keys are never NULL. A NULL operand cannot match an indexed key.
		if value == nil {
			continue
		}
		key, err := c.definition.NormalizeKey(value)
		if err != nil {
			return nil, true, fmt.Errorf("%w: invalid key value: %v", vectordata.ErrInvalidFilter, err)
		}
		// SQLite cannot store invalid UTF-8 keys. Preserve the old filter's
		// no-match behavior without JSON replacing bad bytes with U+FFFD.
		if !validText(key) {
			continue
		}
		token := keyToken(key)
		if _, exists := seen[token]; !exists {
			seen[token] = struct{}{}
			tokens = append(tokens, token)
		}
	}
	return tokens, true, nil
}

func countKeysSQL(size int) string { return `SELECT count(*)` + lookupFromSQL(size) }

func (c *Collection) countKeys(ctx context.Context, tokens []string) (int64, error) {
	// Pin registration validation and every key chunk to one deferred read
	// snapshot. Counts inspect index entries only, including for damaged payloads.
	tx, err := c.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := c.metadata(ctx, tx); err != nil {
		return 0, err
	}
	var total int64
	for start := 0; start < len(tokens); {
		end := start + min(lookupBatchSize, len(tokens)-start)
		args := make([]any, end-start+1)
		args[0] = c.name
		for i, token := range tokens[start:end] {
			args[i+1] = token
		}
		var count int64
		if err := tx.QueryRowContext(ctx, countKeysSQL(end-start), args...).Scan(&count); err != nil {
			return 0, err
		}
		total += count
		start = end
	}
	if err := tx.Rollback(); err != nil {
		return 0, err
	}
	return total, nil
}
