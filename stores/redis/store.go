// Package redis provides model collections in standalone Redis with exact Go
// search and filtering over atomic collection snapshots. It creates no native
// vector/data/full-text indexes. Snapshot size and transfer costs limit scale.
package redis

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

// StoreOptions configures an owned standalone Redis client. TLSConfig is cloned.
// Redis persistence/eviction policy is deployment configuration, not set here.
type StoreOptions struct {
	Addr, Username, Password string
	DB                       int
	TLSConfig                *tls.Config
	// Namespace isolates collections; the default is "vectorstore".
	Namespace string
	// MaxCollectionBytes bounds canonical snapshots and hash publication bytes
	// (manifest, keys, field names/values); default 16 MiB. Redis allocator and
	// peak staging/retirement overhead are outside this encoded byte budget.
	// Larger collections are rejected, never silently truncated.
	MaxCollectionBytes int
	// StorageMode selects the layout for newly created collections. Existing
	// layouts are preserved; use Collection.MigrateToHashStorage for an upgrade.
	StorageMode        StorageMode
	Embedder           vectordata.Embedder
	InputEmbedder      vectordata.InputEmbedder
	EmbeddingBatchSize int
}

type Store struct {
	client      *goredis.Client
	prefix      string
	metadata    vectordata.StoreMetadata
	maxBytes    int
	options     vectordata.ModelCollectionOptions
	storageMode StorageMode
}

var _ vectordata.ModelStore = (*Store)(nil)
var _ vectordata.ModelCollection = (*Collection)(nil)

// New validates local configuration without contacting Redis. Close releases the
// owned client. Automatic command retries are disabled: uncertain writes must not
// replay. Optimistic conflicts explicitly reported by Redis may retry in Go.
func New(opts StoreOptions) (*Store, error) {
	if opts.StorageMode == "" {
		opts.StorageMode = StorageSnapshot
	}
	if opts.StorageMode != StorageSnapshot && opts.StorageMode != StorageHash {
		return nil, fmt.Errorf("%w: unknown Redis storage mode", vectordata.ErrUnsupported)
	}
	if opts.Addr == "" {
		opts.Addr = "localhost:6379"
	}
	if opts.Namespace == "" {
		opts.Namespace = "vectorstore"
	}
	if opts.DB < 0 || len(opts.Namespace) > 256 || !utf8.ValidString(opts.Namespace) || strings.ContainsRune(opts.Namespace, 0) || opts.MaxCollectionBytes < 0 {
		return nil, fmt.Errorf("%w: invalid Redis store options", vectordata.ErrSchemaMismatch)
	}
	if opts.MaxCollectionBytes == 0 {
		opts.MaxCollectionBytes = 16 << 20
	}
	if opts.MaxCollectionBytes > 256<<20 {
		return nil, fmt.Errorf("%w: Redis snapshot limit exceeds 256 MiB", vectordata.ErrSchemaMismatch)
	}
	defaults, err := (vectordata.ModelCollectionOptions{Embedder: opts.Embedder, InputEmbedder: opts.InputEmbedder, EmbeddingBatchSize: opts.EmbeddingBatchSize}).WithDefaults(vectordata.ModelCollectionOptions{})
	if err != nil {
		return nil, err
	}
	var tlsConfig *tls.Config
	if opts.TLSConfig != nil {
		tlsConfig = opts.TLSConfig.Clone()
	}
	client := goredis.NewClient(&goredis.Options{Addr: opts.Addr, Username: opts.Username, Password: opts.Password, DB: opts.DB, TLSConfig: tlsConfig, MaxRetries: -1, ContextTimeoutEnabled: true, DialTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second})
	return &Store{client: client, prefix: "__vectorstore_redis_v1:" + encodeName(opts.Namespace) + ":", maxBytes: opts.MaxCollectionBytes, options: defaults, storageMode: opts.StorageMode, metadata: vectordata.StoreMetadata{SystemName: "redis", DatabaseName: strconv.Itoa(opts.DB), Namespace: opts.Namespace}}, nil
}

func encodeName(name string) string { return base64.RawURLEncoding.EncodeToString([]byte(name)) }
func validName(name string) bool {
	return name != "" && len(name) <= 63 && utf8.ValidString(name) && !strings.ContainsRune(name, 0) && name != "__vectorstore_models"
}
func (s *Store) key(name string) string                  { return s.prefix + encodeName(name) }
func (s *Store) Close() error                            { return s.client.Close() }
func (s *Store) StoreMetadata() vectordata.StoreMetadata { return s.metadata }
func (s *Store) operation(name, op string, err error) error {
	if err == nil {
		return nil
	}
	return &vectordata.OperationError{StoreMetadata: s.metadata, Backend: "redis", Collection: name, Operation: op, Err: err}
}

func (s *Store) ListCollectionNames(ctx context.Context) (names []string, err error) {
	defer func() { err = s.operation("", "list_collections", resolveContext(ctx, err)) }()
	seen := map[string]bool{}
	var cursor uint64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		keys, next, err := s.client.Scan(ctx, cursor, s.prefix+"*", 100).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, s.prefix))
			if err == nil && validName(string(decoded)) {
				seen[string(decoded)] = true
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	names = make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
func (s *Store) CollectionExists(ctx context.Context, name string) (exists bool, err error) {
	defer func() { err = s.operation(name, "collection_exists", resolveContext(ctx, err)) }()
	if !validName(name) {
		return false, vectordata.ErrSchemaMismatch
	}
	n, err := s.client.Exists(ctx, s.key(name)).Result()
	return n != 0, err
}
func (s *Store) DeleteCollection(ctx context.Context, name string) (err error) {
	defer func() { err = s.operation(name, "delete_collection", resolveContext(ctx, err)) }()
	if !validName(name) {
		return vectordata.ErrSchemaMismatch
	}
	if err = s.client.Del(ctx, s.key(name)).Err(); err != nil {
		return err
	}
	// Publication/deletion succeeded. Cleanup is separate and may safely be
	// repeated with PruneHashStorage; it never changes the logical result.
	_, _ = s.PruneHashStorage(ctx, name)
	return nil
}
func (s *Store) GetModelCollection(name string, d vectordata.CollectionDefinition, opts vectordata.ModelCollectionOptions) (vectordata.ModelCollection, error) {
	if !validName(name) {
		return nil, vectordata.ErrSchemaMismatch
	}
	d, err := d.Normalize()
	if err != nil {
		return nil, err
	}
	for _, p := range d.Vectors {
		if p.Kind != "" && p.Kind != vectordata.VectorDense {
			return nil, fmt.Errorf("%w: Redis snapshot provider supports dense vectors", vectordata.ErrUnsupported)
		}
	}
	if !utf8.ValidString(d.Key.Name) || !utf8.ValidString(d.Key.StorageName) {
		return nil, vectordata.ErrSchemaMismatch
	}
	for _, p := range d.Data {
		if !utf8.ValidString(p.Name) || !utf8.ValidString(p.StorageName) {
			return nil, vectordata.ErrSchemaMismatch
		}
	}
	for _, p := range d.Vectors {
		if !utf8.ValidString(p.Name) || !utf8.ValidString(p.StorageName) {
			return nil, vectordata.ErrSchemaMismatch
		}
	}
	opts, err = opts.WithDefaults(s.options)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return &Collection{store: s, name: name, definition: d, registration: string(raw), options: opts}, nil
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

// resolveContext preserves cancellation even when a socket error races its expiry.
func resolveContext(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	return err
}
