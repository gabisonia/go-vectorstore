// Package qdrant provides bounded canonical model snapshots on standalone Qdrant
// 1.20.2. Dense search and filters execute in Go; no native indexes are advertised.
package qdrant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Limits are persisted with each physical collection and must match on reopen.
// Zero fields select defaults. There is no automatic receipt eviction or reset.
type Limits struct {
	MaxRecords         int
	MaxCollectionBytes int
	MaxReceipts        int
}

// StoreOptions configures a client without contacting the server. A supplied
// HTTPClient is copied, remains caller-owned, and must not replay write requests.
type StoreOptions struct {
	URL, APIKey, Namespace string
	HTTPClient             *http.Client
	Limits                 Limits
	Embedder               vectordata.Embedder
	InputEmbedder          vectordata.InputEmbedder
	EmbeddingBatchSize     int
}

type Store struct {
	url, apiKey, namespace string
	http                   *http.Client
	transport              *http.Transport
	limits                 Limits
	options                vectordata.ModelCollectionOptions
	metadata               vectordata.StoreMetadata
	closed                 atomic.Bool
}

type Collection struct {
	store        *Store
	name         string
	definition   vectordata.CollectionDefinition
	registration string
	options      vectordata.ModelCollectionOptions
}

// CollectionUsage reports logical capacity, not server disk/RSS or a reservation.
// Receipt usage includes initialization, lifecycle and changed record writes.
type CollectionUsage struct {
	Active                                    bool
	Records, CanonicalBytes, RetainedReceipts int
	Limits                                    Limits
}

func (s *Store) Usage(ctx context.Context, name string) (usage CollectionUsage, err error) {
	defer func() { err = s.operation(ctx, name, "usage", err) }()
	if !validName(name) {
		return usage, vectordata.ErrSchemaMismatch
	}
	m, err := s.readRoot(ctx, name)
	if err != nil {
		return usage, err
	}
	usage = CollectionUsage{Active: m.Active, Records: len(m.Records), RetainedReceipts: len(m.Receipts), Limits: s.limits}
	for key, raw := range m.Records {
		usage.CanonicalBytes += len(key) + len(raw)
	}
	return usage, nil
}

var _ vectordata.ModelStore = (*Store)(nil)
var _ vectordata.ModelCollection = (*Collection)(nil)

// New validates local configuration without contacting Qdrant. Limits become
// immutable physical metadata when a collection is first created.
func New(opts StoreOptions) (*Store, error) {
	u, err := url.Parse(strings.TrimRight(opts.URL, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: Qdrant requires an http(s) URL without credentials/query/fragment", vectordata.ErrSchemaMismatch)
	}
	if opts.Namespace == "" {
		opts.Namespace = "vectorstore"
	}
	if len(opts.Namespace) > 256 || !utf8.ValidString(opts.Namespace) || strings.ContainsRune(opts.Namespace, 0) {
		return nil, vectordata.ErrSchemaMismatch
	}
	l := opts.Limits
	if l.MaxRecords == 0 {
		l.MaxRecords = 4096
	}
	if l.MaxCollectionBytes == 0 {
		l.MaxCollectionBytes = 4 << 20
	}
	if l.MaxReceipts == 0 {
		l.MaxReceipts = 16384
	}
	if l.MaxRecords < 1 || l.MaxRecords > 65536 || l.MaxCollectionBytes < 1 || l.MaxCollectionBytes > 16<<20 || l.MaxReceipts < 2 || l.MaxReceipts > 262144 {
		return nil, fmt.Errorf("%w: invalid Qdrant snapshot limits", vectordata.ErrSchemaMismatch)
	}
	options, err := (vectordata.ModelCollectionOptions{Embedder: opts.Embedder, InputEmbedder: opts.InputEmbedder, EmbeddingBatchSize: opts.EmbeddingBatchSize}).WithDefaults(vectordata.ModelCollectionOptions{})
	if err != nil {
		return nil, err
	}
	c, transport := newHTTPClient(opts.HTTPClient)
	return &Store{url: u.String(), apiKey: opts.APIKey, namespace: opts.Namespace, http: c, transport: transport, limits: l, options: options,
		metadata: vectordata.StoreMetadata{SystemName: "qdrant", DatabaseName: u.String(), Namespace: opts.Namespace}}, nil
}

// Close releases owned idle connections and rejects subsequent requests.
// Stop application work before closing; in-flight HTTP requests are not canceled.
func (s *Store) Close() error {
	s.closed.Store(true)
	if s.transport != nil {
		s.transport.CloseIdleConnections()
	}
	return nil
}
func (s *Store) StoreMetadata() vectordata.StoreMetadata { return s.metadata }
func (c *Collection) CollectionMetadata() vectordata.CollectionMetadata {
	return vectordata.CollectionMetadata{StoreMetadata: c.store.metadata, CollectionName: c.name}
}
func (c *Collection) Name() string                                { return c.name }
func (c *Collection) Definition() vectordata.CollectionDefinition { return c.definition.Clone() }
func (c *Collection) Exists(ctx context.Context) (bool, error) {
	return c.store.CollectionExists(ctx, c.name)
}
func (c *Collection) EnsureDeleted(ctx context.Context) error {
	return c.store.DeleteCollection(ctx, c.name)
}
func validName(name string) bool {
	return name != "" && len(name) <= 63 && utf8.ValidString(name) && !strings.ContainsRune(name, 0) && name != "__vectorstore_models"
}
func (s *Store) physical(name string) string {
	digest := sha256.Sum256([]byte(s.namespace + "\x00" + name))
	return "go_vectorstore_v1_" + hex.EncodeToString(digest[:])
}
func (s *Store) path(name string) string { return "/collections/" + s.physical(name) }
func (s *Store) operation(ctx context.Context, name, op string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return &vectordata.OperationError{StoreMetadata: s.metadata, Backend: "qdrant", Collection: name, Operation: op, Err: err}
}
func (c *Collection) operation(ctx context.Context, op string, err error) error {
	return c.store.operation(ctx, c.name, op, err)
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
			return nil, fmt.Errorf("%w: Qdrant foundation supports dense vectors", vectordata.ErrUnsupported)
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
	if len(raw) > maxDefinitionBytes {
		return nil, ErrLimit
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

func (s *Store) CollectionExists(ctx context.Context, name string) (exists bool, err error) {
	defer func() { err = s.operation(ctx, name, "collection_exists", err) }()
	if !validName(name) {
		return false, vectordata.ErrSchemaMismatch
	}
	m, err := s.readRoot(ctx, name)
	if errors.Is(err, vectordata.ErrCollectionNotFound) || errors.Is(err, errRootAbsent) {
		return false, nil
	}
	return m.Active, err
}
func (s *Store) DeleteCollection(ctx context.Context, name string) (err error) {
	defer func() { err = s.operation(ctx, name, "delete_collection", err) }()
	if !validName(name) {
		return vectordata.ErrSchemaMismatch
	}
	base, err := s.readRoot(ctx, name)
	if errors.Is(err, vectordata.ErrCollectionNotFound) || errors.Is(err, errRootAbsent) {
		return nil
	}
	if err != nil {
		return err
	}
	epoch := base.Epoch
	for range 64 {
		if !base.Active || base.Epoch != epoch {
			return nil
		}
		next := cloneSnapshot(base)
		next.Active, next.Records = false, map[string]json.RawMessage{}
		err = s.transition(ctx, name, base, next)
		if !errors.Is(err, errConflict) {
			return err
		}
		base, err = s.readRoot(ctx, name)
		if err != nil {
			return err
		}
	}
	return errConflict
}
func (c *Collection) EnsureExists(ctx context.Context) (err error) {
	defer func() { err = c.operation(ctx, "ensure_exists", err) }()
	if err = c.store.ensurePhysical(ctx, c.name); err != nil {
		return err
	}
	base, err := c.store.initialize(ctx, c.name)
	if err != nil {
		return err
	}
	for range 64 {
		if base.Active {
			if base.Definition != c.registration {
				return vectordata.ErrSchemaMismatch
			}
			return nil
		}
		next := cloneSnapshot(base)
		next.Epoch, err = vectordata.NewUUIDKey()
		if err != nil {
			return err
		}
		next.Active, next.Definition, next.Next, next.Records = true, c.registration, 1, map[string]json.RawMessage{}
		err = c.store.transition(ctx, c.name, base, next)
		if !errors.Is(err, errConflict) {
			return err
		}
		base, err = c.store.readRoot(ctx, c.name)
		if err != nil {
			return err
		}
	}
	return errConflict
}

func (s *Store) ListCollectionNames(ctx context.Context) (names []string, err error) {
	defer func() { err = s.operation(ctx, "", "list_collections", err) }()
	if err = s.checkServer(ctx); err != nil {
		return nil, err
	}
	var out response[struct {
		Collections []struct {
			Name string `json:"name"`
		} `json:"collections"`
	}]
	if _, err = s.request(ctx, "GET", "/collections", nil, &out); err != nil {
		return nil, err
	}
	names = []string{}
	for _, item := range out.Result.Collections {
		if !strings.HasPrefix(item.Name, "go_vectorstore_v1_") {
			continue
		}
		var info collectionInfo
		_, readErr := s.request(ctx, "GET", "/collections/"+url.PathEscape(item.Name), nil, &info)
		if statusIs(readErr, http.StatusNotFound) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		var owner physicalOwner
		if json.Unmarshal(info.Result.Config.Metadata["go_vectorstore"], &owner) != nil || owner.Namespace != s.namespace {
			continue
		}
		if !validName(owner.Name) || item.Name != s.physical(owner.Name) {
			return nil, ErrCorrupt
		}
		m, readErr := s.readRoot(ctx, owner.Name)
		if errors.Is(readErr, vectordata.ErrCollectionNotFound) || errors.Is(readErr, errRootAbsent) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		if m.Active {
			names = append(names, owner.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// ConfirmWrite performs read-only reconciliation of an OutcomeUnknownError.
// Absence of the token remains unknown: an original request can still be in flight.
func (s *Store) ConfirmWrite(ctx context.Context, name, token string) (err error) {
	defer func() { err = s.operation(ctx, name, "confirm_write", err) }()
	if !validName(name) || token == "" {
		return vectordata.ErrInvalidRecord
	}
	m, err := s.readRoot(ctx, name)
	if err != nil {
		return unknown(token, err)
	}
	for _, receipt := range m.Receipts {
		if receipt == token {
			return nil
		}
	}
	return unknown(token, nil)
}

func (c *Collection) load(ctx context.Context) (snapshot, []byte, error) {
	m, err := c.store.readRoot(ctx, c.name)
	if err != nil {
		return snapshot{}, nil, err
	}
	if !m.Active {
		return snapshot{}, nil, vectordata.ErrCollectionNotFound
	}
	if m.Definition != c.registration {
		return snapshot{}, nil, vectordata.ErrSchemaMismatch
	}
	return m, nil, nil
}
func (c *Collection) mutate(ctx context.Context, apply func(*snapshot) error) error {
	return c.mutateEpoch(ctx, "", apply)
}
func (c *Collection) mutateEpoch(ctx context.Context, epoch string, apply func(*snapshot) error) error {
	base, _, err := c.load(ctx)
	if err != nil {
		return err
	}
	if epoch == "" {
		epoch = base.Epoch
	}
	for range 64 {
		if !base.Active || base.Epoch != epoch {
			return vectordata.ErrCollectionNotFound
		}
		next := cloneSnapshot(base)
		if err = apply(&next); err != nil {
			return err
		}
		if base.Next == next.Next && reflect.DeepEqual(base.Records, next.Records) {
			return ctx.Err()
		}
		if err := c.store.validate(next); err != nil {
			return err
		}
		// Validate the complete resulting canonical dataset before publication,
		// including unchanged records. Reads/embeddings never repair corruption.
		if _, err := c.decode(ctx, next, nil); err != nil {
			return err
		}
		err = c.store.transition(ctx, c.name, base, next)
		if !errors.Is(err, errConflict) {
			return err
		}
		base, _, err = c.load(ctx)
		if err != nil {
			return err
		}
	}
	return errConflict
}
