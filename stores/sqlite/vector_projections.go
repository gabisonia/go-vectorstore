package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func initializeVectorProjections(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS __vectorstore_vector_projections (
 collection TEXT PRIMARY KEY REFERENCES __vectorstore_collections(name) ON DELETE CASCADE,
 definition TEXT NOT NULL, version INTEGER NOT NULL CHECK(version=1), ready INTEGER NOT NULL CHECK(ready IN (0,1)))`,
		`CREATE TABLE IF NOT EXISTS __vectorstore_vector_values (
 collection TEXT NOT NULL, property TEXT NOT NULL, key TEXT NOT NULL, vector BLOB NOT NULL,
 PRIMARY KEY(collection,property,key),
 FOREIGN KEY(collection,key) REFERENCES __vectorstore_records(collection,key) ON DELETE CASCADE,
 FOREIGN KEY(collection) REFERENCES __vectorstore_vector_projections(collection) ON DELETE CASCADE) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS __vectorstore_vector_key ON __vectorstore_vector_values(collection,key)`,
		// These ordinary SQL triggers must work in older/unextended binaries too.
		`CREATE TRIGGER IF NOT EXISTS __vectorstore_vector_insert AFTER INSERT ON __vectorstore_records BEGIN
 UPDATE __vectorstore_vector_projections SET ready=0 WHERE collection=NEW.collection; END`,
		`CREATE TRIGGER IF NOT EXISTS __vectorstore_vector_update AFTER UPDATE ON __vectorstore_records BEGIN
 UPDATE __vectorstore_vector_projections SET ready=0 WHERE collection=OLD.collection OR collection=NEW.collection; END`,
		`CREATE TRIGGER IF NOT EXISTS __vectorstore_vector_delete AFTER DELETE ON __vectorstore_records BEGIN
 UPDATE __vectorstore_vector_projections SET ready=0 WHERE collection=OLD.collection; END`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// Read readiness and canonical rows in the same transaction. Definition/version
// mismatch and dirty/absent projections retain canonical behavior until rebuild.
func (c *Collection) vectorProjectionsReady(ctx context.Context, tx *sql.Tx) (bool, error) {
	if !c.store.vectorProjections {
		return false, nil
	}
	var definition string
	var version, ready int
	err := tx.QueryRowContext(ctx, `SELECT definition,version,ready FROM __vectorstore_vector_projections WHERE collection=?`, c.name).Scan(&definition, &version, &ready)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return ready == 1 && version == 1 && definition == c.registration, err
}

func (c *Collection) publishVectorProjections(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE __vectorstore_vector_projections SET ready=1 WHERE collection=?`, c.name)
	return err
}

func packVector(vector []float32) []byte {
	packed := make([]byte, 4*len(vector))
	for i, value := range vector {
		binary.LittleEndian.PutUint32(packed[i*4:], math.Float32bits(value))
	}
	return packed
}

// Without a requirement SQLite can prefer a collection-only scan of the
// WITHOUT ROWID primary key over this point index, especially without ANALYZE
// statistics. That makes insert/upsert/rebuild cleanup quadratic and repeatedly
// reads large vector overflow pages even when the requested key is absent.
const deleteVectorProjectionsSQL = `DELETE FROM __vectorstore_vector_values INDEXED BY __vectorstore_vector_key WHERE collection=? AND key=?`

func (c *Collection) projectVectors(ctx context.Context, tx *sql.Tx, record vectordata.ModelRecord) error {
	token := keyToken(record.Key)
	if _, err := tx.ExecContext(ctx, deleteVectorProjectionsSQL, c.name, token); err != nil {
		return err
	}
	for _, property := range c.definition.Vectors {
		if property.Kind == vectordata.VectorBinary || property.Kind == vectordata.VectorSparse {
			continue
		}
		if vector := record.Vectors[property.Name]; vector != nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO __vectorstore_vector_values(collection,property,key,vector) VALUES(?,?,?,?)`, c.name, property.Name, token, packVector(vector)); err != nil {
				return err
			}
		}
	}
	return nil
}

// RebuildVectorProjections explicitly validates/backfills dense float32/float16
// vectors and publishes readiness in one immediate write transaction. It retains
// at most 128 decoded canonical records at once and never invokes embedders.
// VectorProjections must be enabled. An error rolls back the complete rebuild.
func (c *Collection) RebuildVectorProjections(ctx context.Context) (err error) {
	defer func() { err = c.operation("rebuild_vector_projections", err) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.store.vectorProjections {
		return vectordata.ErrUnsupported
	}
	tx, err := c.store.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := c.metadata(ctx, tx); err != nil {
		return err
	}
	if err := c.rebuildVectorProjections(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Collection) rebuildVectorProjections(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO __vectorstore_vector_projections(collection,definition,version,ready) VALUES(?,?,1,0)
 ON CONFLICT(collection) DO UPDATE SET definition=excluded.definition,version=1,ready=0`, c.name, c.registration); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM __vectorstore_vector_values WHERE collection=?`, c.name); err != nil {
		return err
	}
	// Keyset batches close the canonical cursor before writing projection rows.
	// JSON key tokens are never empty; ordering here affects batching only.
	after := ""
	for {
		records, last, err := c.vectorProjectionBatch(ctx, tx, after)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := c.projectVectors(ctx, tx, record); err != nil {
				return err
			}
		}
		if len(records) < 128 {
			break
		}
		after = last
	}
	return c.publishVectorProjections(ctx, tx)
}

func (c *Collection) vectorProjectionBatch(ctx context.Context, tx *sql.Tx, after string) ([]vectordata.ModelRecord, string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key,payload FROM __vectorstore_records WHERE collection=? AND key>? ORDER BY key LIMIT 128`, c.name, after)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	records := make([]vectordata.ModelRecord, 0, 128)
	d := c.readDefinition()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&after, &payload); err != nil {
			return nil, "", err
		}
		record, err := decodeRecord(payload, d)
		if err != nil {
			return nil, "", err
		}
		prepared, err := vectordata.PrepareModelRecords(ctx, d, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{record})
		if err != nil {
			return nil, "", err
		}
		if keyToken(prepared[0].Key) != after {
			return nil, "", fmt.Errorf("%w: canonical key differs from stored key", vectordata.ErrInvalidRecord)
		}
		records = append(records, prepared[0])
	}
	return records, after, rows.Err()
}

// The scalar value index drives the join. Each matching key probes one packed
// vector, so a missing optional vector drops out before ranking and pagination.
const indexedVectorEqualitySQL = `SELECT v.key,v.vector
 FROM __vectorstore_scalar_values AS s INDEXED BY __vectorstore_scalar_lookup
 CROSS JOIN __vectorstore_vector_values AS v
 WHERE s.collection=?1 AND s.property=?2 AND s.value IS ?3
 AND v.collection=?1 AND v.property=?4 AND v.key=s.key`

// Resolve the entire predicate before acquiring the connection. Only a single
// equality on a registered scalar has equivalent membership without a residual.
func (c *Collection) indexedVectorEquality(filter vectordata.Filter) (vectordata.EqFilter, bool, error) {
	eq, ok := filter.(vectordata.EqFilter)
	if !ok || !c.store.nativeIndexes {
		return eq, false, nil
	}
	ref, err := vectordata.NormalizeFieldRef(eq.Field)
	if err != nil {
		return eq, false, err
	}
	kind, indexed := c.scalarIndexProperties()[ref.Name]
	if ref.Kind != vectordata.FieldColumn || !indexed {
		return eq, false, nil
	}
	compiler := scalarQueryCompiler{definition: c.definition}
	value, err := compiler.normalize(ref.Name, vectordata.DataProperty{Name: ref.Name, Type: kind}, eq.Value)
	if err != nil || !validText(value) {
		return eq, false, err
	}
	return vectordata.EqFilter{Field: ref, Value: value}, true, nil
}

func (c *Collection) searchProjectedVectors(ctx context.Context, p vectordata.VectorProperty, top int, opts vectordata.ModelSearchOptions, measure exactsearch.Measure, denseQuery []float32) ([]vectordata.ModelSearchResult, bool, error) {
	if !c.store.vectorProjections || p.Kind == vectordata.VectorBinary || p.Kind == vectordata.VectorSparse {
		return nil, false, nil
	}
	eq, filtered, err := c.indexedVectorEquality(opts.Filter)
	if err != nil || (opts.Filter != nil && !filtered) {
		return nil, false, err
	}
	tx, err := c.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, true, err
	}
	defer tx.Rollback()
	if _, err := c.metadata(ctx, tx); err != nil {
		return nil, true, err
	}
	ready, err := c.vectorProjectionsReady(ctx, tx)
	if err != nil || !ready {
		return nil, false, err
	}
	// Rank only keys and one packed vector. Fetch the full selected page from
	// this same WAL snapshot after the scan cursor closes.
	rankOpts := opts
	rankOpts.IncludeVectors = false
	d := vectordata.CollectionDefinition{Key: c.definition.Key, Vectors: []vectordata.VectorProperty{p}}
	source := c.projectedVectorScan(ctx, tx, p)
	if filtered {
		ready, err := c.scalarIndexesReady(ctx, tx)
		if errors.Is(err, vectordata.ErrSchemaMismatch) {
			return nil, false, nil
		}
		if err != nil || !ready {
			return nil, false, err
		}
		source = c.scanProjectedVectorsSQL(ctx, tx, p, indexedVectorEqualitySQL, c.name, eq.Field.Name, eq.Value, p.Name)
		rankOpts.Filter = nil // The equality is fully evaluated by the scalar lookup.
	} else if c.store.nativeVectors && p.Metric == vectordata.DistanceL1 && nativeL1Platform() && denseQuery != nil {
		source = c.scanProjectedVectorsSQL(ctx, tx, p, nativeL1CandidatesSQL, c.name, p.Name, packVector(denseQuery), top+opts.Skip-1)
	}
	hits, err := exactsearch.Run(ctx, d, p, top, rankOpts, source, measure)
	if err != nil {
		return nil, true, err
	}
	for start := 0; start < len(hits); {
		end := start + min(lookupBatchSize, len(hits)-start)
		tokens := make([]string, end-start)
		for i := range tokens {
			tokens[i] = keyToken(hits[start+i].Record.Key)
		}
		records, err := c.lookupBatch(ctx, tx, tokens, vectordata.RetrievalOptions{IncludeVectors: opts.IncludeVectors})
		if err != nil {
			return nil, true, err
		}
		byKey := make(map[string]vectordata.ModelRecord, len(records))
		for _, record := range records {
			byKey[keyToken(record.Key)] = record
		}
		for i, token := range tokens {
			record, exists := byKey[token]
			if !exists {
				return nil, true, fmt.Errorf("%w: projected key has no canonical record", vectordata.ErrInvalidRecord)
			}
			hits[start+i].Record = record
		}
		start = end
	}
	return hits, true, ctx.Err()
}

func (c *Collection) projectedVectorScan(ctx context.Context, tx *sql.Tx, p vectordata.VectorProperty) iter.Seq2[vectordata.ModelRecord, error] {
	return c.scanProjectedVectorsSQL(ctx, tx, p, `SELECT key,vector FROM __vectorstore_vector_values WHERE collection=? AND property=?`, c.name, p.Name)
}

func (c *Collection) scanProjectedVectorsSQL(ctx context.Context, tx *sql.Tx, p vectordata.VectorProperty, query string, args ...any) iter.Seq2[vectordata.ModelRecord, error] {
	return func(yield func(vectordata.ModelRecord, error) bool) {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			yield(vectordata.ModelRecord{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var token string
			var packed []byte
			if err := rows.Scan(&token, &packed); err != nil {
				yield(vectordata.ModelRecord{}, err)
				return
			}
			record, err := c.unpackProjectedVector(token, packed, p)
			if !yield(record, err) || err != nil {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(vectordata.ModelRecord{}, err)
		}
	}
}

func (c *Collection) unpackProjectedVector(token string, packed []byte, p vectordata.VectorProperty) (vectordata.ModelRecord, error) {
	var key any
	switch c.definition.Key.Type {
	case vectordata.KeyInt64:
		key = new(int64)
	case vectordata.KeyUint64:
		key = new(uint64)
	default:
		key = new(string)
	}
	if err := json.Unmarshal([]byte(token), key); err != nil {
		return vectordata.ModelRecord{}, fmt.Errorf("%w: projected key: %v", vectordata.ErrInvalidRecord, err)
	}
	switch value := key.(type) {
	case *int64:
		key = *value
	case *uint64:
		key = *value
	case *string:
		key = *value
	}
	if len(packed) != 4*p.Dimensions {
		return vectordata.ModelRecord{}, fmt.Errorf("%w: projected vector length", vectordata.ErrDimensionMismatch)
	}
	vector := make([]float32, p.Dimensions)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(packed[i*4:]))
	}
	vector, err := vectordata.NormalizeModelVector(vector, p)
	return vectordata.ModelRecord{Key: key, Vectors: map[string][]float32{p.Name: vector}}, err
}
