// Package sqlite provides durable embedded model collections with exact search.
// Get/GetMany and unfiltered Count use direct SQL. Query/filtered Count materialize
// a snapshot; exact search streams a read snapshot through bounded ranking.
// No native ANN, full-text
// indexes, hybrid search, or automatic schema migrations are provided.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	driver "modernc.org/sqlite"
)

// StoreOptions configures runtime embedding defaults. SQLite uses WAL mode,
// FULL synchronization, foreign keys, and context-aware lock waits up to five seconds.
type StoreOptions struct {
	Embedder           vectordata.Embedder
	EmbeddingBatchSize int
}

type Store struct {
	db      *sql.DB
	options vectordata.ModelCollectionOptions
}

var _ vectordata.ModelStore = (*Store)(nil)
var _ vectordata.CapabilityProvider = (*Store)(nil)

// Open opens a database file (or a private :memory: database), creating the
// provider tables if needed. Paths are literal filenames, not driver DSNs.
// Close must be called when the store is no longer needed.
func Open(ctx context.Context, path string, opts StoreOptions) (*Store, error) {
	defaults, err := (vectordata.ModelCollectionOptions{Embedder: opts.Embedder, EmbeddingBatchSize: opts.EmbeddingBatchSize}).WithDefaults(vectordata.ModelCollectionOptions{})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("%w: database path is empty", vectordata.ErrSchemaMismatch)
	}
	var dsn string
	if path == ":memory:" {
		dsn = path
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		dsn = (&url.URL{Scheme: "file", Path: absolute}).String()
	}
	q := url.Values{}
	// Disable native sleeping: retryBusy supplies bounded, cancelable waits.
	q.Add("_pragma", "busy_timeout(0)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", dsn+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, options: defaults}
	if err := s.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) initialize(ctx context.Context) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS __vectorstore_collections (name TEXT PRIMARY KEY, definition TEXT NOT NULL, next_key INTEGER NOT NULL DEFAULT 1, format INTEGER NOT NULL DEFAULT 1 CHECK (format=1))`,
		`CREATE TABLE IF NOT EXISTS __vectorstore_records (collection TEXT NOT NULL REFERENCES __vectorstore_collections(name) ON DELETE CASCADE, key TEXT NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(collection,key))`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Close() error { return s.db.Close() }
func (*Store) Capabilities() vectordata.ProviderCapabilities {
	caps := memory.New().Capabilities()
	caps.Backend = "sqlite"
	return caps
}
func (s *Store) ListCollectionNames(ctx context.Context) ([]string, error) {
	return retryBusy(ctx, func() ([]string, error) { return s.listCollectionNames(ctx) })
}
func (s *Store) listCollectionNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM __vectorstore_collections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
func (s *Store) CollectionExists(ctx context.Context, name string) (bool, error) {
	return retryBusy(ctx, func() (bool, error) {
		var exists bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM __vectorstore_collections WHERE name=?)`, name).Scan(&exists)
		return exists, err
	})
}
func (s *Store) DeleteCollection(ctx context.Context, name string) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM __vectorstore_collections WHERE name=?`, name); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) GetModelCollection(name string, d vectordata.CollectionDefinition, opts vectordata.ModelCollectionOptions) (vectordata.ModelCollection, error) {
	opts, err := opts.WithDefaults(s.options)
	if err != nil {
		return nil, err
	}
	if name == "" || !utf8.ValidString(name) || len(name) > 63 || strings.ContainsRune(name, 0) || strings.HasPrefix(name, "__vectorstore_") {
		return nil, fmt.Errorf("%w: invalid collection name", vectordata.ErrSchemaMismatch)
	}
	d, err = d.Normalize()
	if err != nil {
		return nil, err
	}
	names := []string{d.Key.Name, d.Key.StorageName}
	for _, p := range d.Data {
		names = append(names, p.Name, p.StorageName)
	}
	for _, p := range d.Vectors {
		names = append(names, p.Name, p.StorageName)
	}
	for _, name := range names {
		if !utf8.ValidString(name) {
			return nil, fmt.Errorf("%w: model names require UTF-8", vectordata.ErrSchemaMismatch)
		}
	}
	definition, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return &Collection{store: s, name: name, definition: d, registration: string(definition), options: opts}, nil
}
func (s *Store) EnsureModelCollection(ctx context.Context, name string, d vectordata.CollectionDefinition, opts vectordata.ModelCollectionOptions) (vectordata.ModelCollection, error) {
	c, err := s.GetModelCollection(name, d, opts)
	if err != nil {
		return nil, err
	}
	if err = c.EnsureExists(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

type Collection struct {
	store        *Store
	name         string
	definition   vectordata.CollectionDefinition
	registration string
	options      vectordata.ModelCollectionOptions
}

var _ vectordata.ModelCollection = (*Collection)(nil)
var _ vectordata.BinarySearcher = (*Collection)(nil)
var _ vectordata.SparseSearcher = (*Collection)(nil)

func (c *Collection) Name() string                                { return c.name }
func (c *Collection) Definition() vectordata.CollectionDefinition { return c.definition.Clone() }
func (c *Collection) Exists(ctx context.Context) (bool, error) {
	return c.store.CollectionExists(ctx, c.name)
}
func (c *Collection) EnsureDeleted(ctx context.Context) error {
	return c.store.DeleteCollection(ctx, c.name)
}
func (c *Collection) operation(op string, err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *driver.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&255 == 19 {
		err = fmt.Errorf("%w: %w", vectordata.ErrInvalidRecord, err)
	}
	return &vectordata.OperationError{Backend: "sqlite", Collection: c.name, Operation: op, Err: err}
}
func (c *Collection) checkDefinition(registration string, format int) error {
	if format != 1 || registration != c.registration {
		return fmt.Errorf("%w: stored collection definition or format differs", vectordata.ErrSchemaMismatch)
	}
	return nil
}
func (c *Collection) metadata(ctx context.Context, tx *sql.Tx) (int64, error) {
	var registration string
	var format int
	var next int64
	err := tx.QueryRowContext(ctx, `SELECT definition,format,next_key FROM __vectorstore_collections WHERE name=?`, c.name).Scan(&registration, &format, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, vectordata.ErrCollectionNotFound
	}
	if err != nil {
		return 0, err
	}
	return next, c.checkDefinition(registration, format)
}
func (c *Collection) EnsureExists(ctx context.Context) (err error) {
	defer func() { err = c.operation("ensure", err) }()
	tx, err := c.store.beginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO __vectorstore_collections(name,definition) VALUES(?,?) ON CONFLICT(name) DO NOTHING`, c.name, c.registration); err != nil {
		return err
	}
	if _, err = c.metadata(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (c *Collection) Insert(ctx context.Context, records []vectordata.ModelRecord) ([]any, error) {
	return c.write(ctx, records, false)
}
func (c *Collection) Upsert(ctx context.Context, records []vectordata.ModelRecord) ([]any, error) {
	return c.write(ctx, records, true)
}
func keyToken(key any) string { raw, _ := json.Marshal(key); return string(raw) }
func (c *Collection) write(ctx context.Context, records []vectordata.ModelRecord, upsert bool) (keys []any, err error) {
	defer func() { err = c.operation("write", err) }()
	prepared, err := vectordata.PrepareModelRecords(ctx, c.definition, c.options, records)
	if err != nil {
		return nil, err
	}
	if len(prepared) == 0 {
		return []any{}, ctx.Err()
	}
	tx, err := c.store.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	next, err := c.metadata(ctx, tx)
	if err != nil {
		return nil, err
	}
	keys = make([]any, len(prepared))
	for i, r := range prepared {
		generated := r.Key == nil
		if generated {
			if next == 0 {
				return nil, fmt.Errorf("%w: generated key sequence exhausted", vectordata.ErrInvalidRecord)
			}
			r.Key = next
			if next == math.MaxInt64 {
				next = 0
			} else {
				next++
			}
		}
		payload, err := encodeRecord(r)
		if err != nil {
			return nil, err
		}
		statement := `INSERT INTO __vectorstore_records(collection,key,payload) VALUES(?,?,?)`
		if upsert && !generated {
			statement += ` ON CONFLICT(collection,key) DO UPDATE SET payload=excluded.payload`
		}
		if _, err = tx.ExecContext(ctx, statement, c.name, keyToken(r.Key), payload); err != nil {
			return nil, err
		}
		keys[i] = r.Key
	}
	if _, err = tx.ExecContext(ctx, `UPDATE __vectorstore_collections SET next_key=? WHERE name=?`, next, c.name); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return keys, nil
}
func (c *Collection) Delete(ctx context.Context, keys []any) (count int64, err error) {
	defer func() { err = c.operation("delete", err) }()
	normalized := make([]string, len(keys))
	for i, key := range keys {
		normalized[i], err = c.lookupToken(key)
		if err != nil {
			return 0, err
		}
	}
	if len(keys) == 0 {
		return 0, ctx.Err()
	}
	tx, err := c.store.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = c.metadata(ctx, tx); err != nil {
		return 0, err
	}
	for _, key := range normalized {
		result, e := tx.ExecContext(ctx, `DELETE FROM __vectorstore_records WHERE collection=? AND key=?`, c.name, key)
		if e != nil {
			return 0, e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return 0, e
		}
		count += n
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
