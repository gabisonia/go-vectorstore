package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Stay below SQLite's historical 999-parameter limit, including collection.
const lookupBatchSize = 500

const countAllSQL = `SELECT c.definition,c.format,
 (SELECT count(*) FROM __vectorstore_records r WHERE r.collection=c.name)
 FROM __vectorstore_collections c WHERE c.name=?`

func (c *Collection) countAll(ctx context.Context) (int64, error) {
	var registration string
	var format int
	var count int64
	err := c.store.db.QueryRowContext(ctx, countAllSQL, c.name).Scan(&registration, &format, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, vectordata.ErrCollectionNotFound
	}
	if err != nil {
		return 0, err
	}
	if err := c.checkDefinition(registration, format); err != nil {
		return 0, err
	}
	return count, nil
}

func (c *Collection) lookupToken(key any) (string, error) {
	normalized, err := c.definition.NormalizeKey(key)
	if err != nil {
		return "", err
	}
	// JSON replaces invalid UTF-8 with U+FFFD. Reject invalid text so a lookup or
	// deletion cannot accidentally address a different, valid replacement-character key.
	if !validText(normalized) {
		return "", fmt.Errorf("%w: key requires UTF-8", vectordata.ErrInvalidRecord)
	}
	return keyToken(normalized), nil
}

func lookupSQL(size int) string {
	return `SELECT payload FROM __vectorstore_records WHERE collection=? AND key IN (` + strings.TrimSuffix(strings.Repeat("?,", size), ",") + `)`
}

func (c *Collection) GetMany(ctx context.Context, keys []any, opts vectordata.RetrievalOptions) (records []vectordata.ModelRecord, err error) {
	defer func() { err = c.operation("get_many", err) }()
	tokens := make([]string, 0, min(len(keys), lookupBatchSize))
	seen := make(map[string]struct{}, min(len(keys), lookupBatchSize))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := c.lookupToken(key)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	if len(tokens) == 0 {
		return []vectordata.ModelRecord{}, ctx.Err()
	}
	return retryBusy(ctx, func() ([]vectordata.ModelRecord, error) { return c.lookupMany(ctx, tokens, opts) })
}

func (c *Collection) lookupMany(ctx context.Context, tokens []string, opts vectordata.RetrievalOptions) ([]vectordata.ModelRecord, error) {
	// ReadOnly selects a deferred read transaction even though writes use
	// _txlock=immediate. Pin metadata and every chunk to one WAL snapshot without
	// reserving the writer lock. Retrying this entire read is side-effect-free.
	tx, err := c.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := c.metadata(ctx, tx); err != nil {
		return nil, err
	}
	records := []vectordata.ModelRecord{}
	for start := 0; start < len(tokens); {
		end := start + min(lookupBatchSize, len(tokens)-start)
		batch, err := c.lookupBatch(ctx, tx, tokens[start:end], opts)
		if err != nil {
			return nil, err
		}
		records = append(records, batch...)
		start = end
	}
	// End the read transaction before exposing any results or releasing its
	// connection. Rollback is sufficient: this transaction never writes.
	if err := tx.Rollback(); err != nil {
		return nil, err
	}
	return records, nil
}

func (c *Collection) lookupBatch(ctx context.Context, tx *sql.Tx, tokens []string, opts vectordata.RetrievalOptions) ([]vectordata.ModelRecord, error) {
	args := make([]any, len(tokens)+1)
	args[0] = c.name
	for i, token := range tokens {
		args[i+1] = token
	}
	rows, err := tx.QueryContext(ctx, lookupSQL(len(tokens)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []vectordata.ModelRecord{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		r, err := decodeRecord(payload, c.definition)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Validate only selected records using the same rules as snapshot replay,
	// with embedding sources cleared. Prepared values own their maps and slices.
	records, err = vectordata.PrepareModelRecords(ctx, c.readDefinition(), vectordata.ModelCollectionOptions{}, records)
	if err != nil {
		return nil, err
	}
	if !opts.IncludeVectors {
		for i := range records {
			records[i].Vectors = nil
			records[i].BinaryVectors = nil
			records[i].SparseVectors = nil
		}
	}
	return records, nil
}
