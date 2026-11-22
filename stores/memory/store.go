// Package memory implements exact model collections for development and contract tests.
// It does not replace integration testing against the production database.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type state struct {
	registration string
	records      map[any]vectordata.ModelRecord
	next         int64
}
type Store struct {
	mu          sync.RWMutex
	collections map[string]*state
	options     vectordata.ModelCollectionOptions
}

func New() *Store { return &Store{collections: map[string]*state{}} }

// StoreOptions configures runtime defaults for model collections.
type StoreOptions struct {
	Embedder      vectordata.Embedder
	InputEmbedder vectordata.InputEmbedder
	// EmbeddingBatchSize bounds embedding requests; zero leaves them unbounded.
	EmbeddingBatchSize int
}

// NewWithOptions creates a store with default embedding settings. New retains its
// original behavior. Collection and property embedders can override this default.
func NewWithOptions(opts StoreOptions) (*Store, error) {
	defaults, err := (vectordata.ModelCollectionOptions{Embedder: opts.Embedder, InputEmbedder: opts.InputEmbedder, EmbeddingBatchSize: opts.EmbeddingBatchSize}).WithDefaults(vectordata.ModelCollectionOptions{})
	if err != nil {
		return nil, err
	}
	return &Store{collections: map[string]*state{}, options: defaults}, nil
}

var _ vectordata.ModelStore = (*Store)(nil)
var _ vectordata.InputSearcher = (*Collection)(nil)

func (s *Store) ListCollectionNames(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := []string{}
	for name := range s.collections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
func (s *Store) CollectionExists(ctx context.Context, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.collections[name]
	return exists, nil
}
func (s *Store) DeleteCollection(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.collections, name)
	return nil
}
func (s *Store) GetModelCollection(name string, d vectordata.CollectionDefinition, opts vectordata.ModelCollectionOptions) (vectordata.ModelCollection, error) {
	opts, err := opts.WithDefaults(s.options)
	if err != nil {
		return nil, err
	}
	if name == "" || len(name) > 63 || strings.ContainsRune(name, 0) || name == "__vectorstore_models" {
		return nil, fmt.Errorf("%w: invalid collection name", vectordata.ErrSchemaMismatch)
	}
	definition, err := d.Normalize()
	if err != nil {
		return nil, err
	}
	return &Collection{store: s, name: name, definition: definition, registration: definitionJSON(definition), options: opts}, nil
}
func (s *Store) EnsureModelCollection(ctx context.Context, name string, d vectordata.CollectionDefinition, opts vectordata.ModelCollectionOptions) (vectordata.ModelCollection, error) {
	c, err := s.GetModelCollection(name, d, opts)
	if err != nil {
		return nil, err
	}
	if err := c.EnsureExists(ctx); err != nil {
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

func (c *Collection) Name() string                                { return c.name }
func (c *Collection) Definition() vectordata.CollectionDefinition { return c.definition.Clone() }
func (c *Collection) Exists(ctx context.Context) (bool, error) {
	return c.store.CollectionExists(ctx, c.name)
}
func (c *Collection) EnsureDeleted(ctx context.Context) error {
	return c.store.DeleteCollection(ctx, c.name)
}
func definitionJSON(d vectordata.CollectionDefinition) string {
	d = d.Clone()
	for i := range d.Vectors {
		d.Vectors[i].Embedder = nil
		d.Vectors[i].InputEmbedder = nil
	}
	raw, _ := json.Marshal(d)
	return string(raw)
}
func (c *Collection) EnsureExists(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	if current, exists := c.store.collections[c.name]; exists {
		if current.registration != c.registration {
			return fmt.Errorf("%w: collection definition differs", vectordata.ErrSchemaMismatch)
		}
		return nil
	}
	if c.store.collections == nil {
		c.store.collections = map[string]*state{}
	}
	c.store.collections[c.name] = &state{registration: c.registration, records: map[any]vectordata.ModelRecord{}, next: 1}
	return nil
}
func (c *Collection) Insert(ctx context.Context, records []vectordata.ModelRecord) ([]any, error) {
	return c.write(ctx, records, false)
}
func (c *Collection) Upsert(ctx context.Context, records []vectordata.ModelRecord) ([]any, error) {
	return c.write(ctx, records, true)
}
func (c *Collection) write(ctx context.Context, records []vectordata.ModelRecord, upsert bool) ([]any, error) {
	if len(records) > 0 {
		if err := c.checkSchema(ctx); err != nil {
			return nil, err
		}
	}
	prepared, err := vectordata.PrepareModelRecords(ctx, c.definition, c.options, records)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return []any{}, nil
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	s, err := c.stateLocked()
	if err != nil {
		return nil, err
	}
	staged := make(map[any]vectordata.ModelRecord, len(s.records)+len(prepared))
	for key, r := range s.records {
		staged[key] = r
	}
	keys := make([]any, len(prepared))
	next := s.next
	for i, r := range prepared {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		generated := r.Key == nil
		if generated {
			if c.definition.Key.Type == vectordata.KeyUUID {
				r.Key, err = vectordata.NewUUIDKey()
				if err != nil {
					return nil, err
				}
			} else {
				if next <= 0 {
					return nil, fmt.Errorf("%w: generated key sequence exhausted", vectordata.ErrInvalidRecord)
				}
				r.Key = next
				if next == math.MaxInt64 {
					next = 0
				} else {
					next++
				}
			}
		}
		if _, exists := staged[r.Key]; exists && (!upsert || generated) {
			return nil, fmt.Errorf("%w: duplicate key", vectordata.ErrInvalidRecord)
		}
		staged[r.Key] = r
		keys[i] = r.Key
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.records = staged
	s.next = next
	return keys, nil
}
func (c *Collection) snapshot(ctx context.Context, vectors bool) ([]vectordata.ModelRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.store.mu.RLock()
	defer c.store.mu.RUnlock()
	s, err := c.stateLocked()
	if err != nil {
		return nil, err
	}
	records := make([]vectordata.ModelRecord, 0, len(s.records))
	for _, r := range s.records {
		records = append(records, vectordata.CloneModelRecord(r, c.definition, vectors))
	}
	return records, nil
}
func (c *Collection) Get(ctx context.Context, key any, opts vectordata.RetrievalOptions) (vectordata.ModelRecord, error) {
	normalized, err := c.definition.NormalizeKey(key)
	if err != nil {
		return vectordata.ModelRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return vectordata.ModelRecord{}, err
	}
	c.store.mu.RLock()
	defer c.store.mu.RUnlock()
	s, err := c.stateLocked()
	if err != nil {
		return vectordata.ModelRecord{}, err
	}
	r, exists := s.records[normalized]
	if !exists {
		return vectordata.ModelRecord{}, vectordata.ErrNotFound
	}
	return vectordata.CloneModelRecord(r, c.definition, opts.IncludeVectors), nil
}
func (c *Collection) GetMany(ctx context.Context, keys []any, opts vectordata.RetrievalOptions) ([]vectordata.ModelRecord, error) {
	if len(keys) == 0 {
		return []vectordata.ModelRecord{}, ctx.Err()
	}
	wanted := make(map[any]struct{}, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := c.definition.NormalizeKey(key)
		if err != nil {
			return nil, err
		}
		wanted[value] = struct{}{}
	}
	records, err := c.lookupKeys(ctx, wanted)
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		return vectordata.CompareModelValues(records[i].Key, records[j].Key) < 0
	})
	for i, r := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		records[i] = vectordata.CloneModelRecord(r, c.definition, opts.IncludeVectors)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// Stored records are immutable. Borrow all matched records under one lock and
// release it before sorting or copying their data into caller-owned results.
func (c *Collection) lookupKeys(ctx context.Context, wanted map[any]struct{}) ([]vectordata.ModelRecord, error) {
	c.store.mu.RLock()
	defer c.store.mu.RUnlock()
	state, err := c.stateLocked()
	if err != nil {
		return nil, err
	}
	records := make([]vectordata.ModelRecord, 0, min(len(wanted), len(state.records)))
	for key := range wanted {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if record, ok := state.records[key]; ok {
			records = append(records, record)
		}
	}
	return records, nil
}
func (c *Collection) Query(ctx context.Context, filter vectordata.Filter, top int, opts vectordata.QueryOptions) ([]vectordata.ModelRecord, error) {
	if err := vectordata.ValidateModelQuery(c.definition, top, opts); err != nil {
		return nil, err
	}
	if err := vectordata.ValidateModelFilter(filter, c.definition); err != nil {
		return nil, err
	}
	fields := map[string]bool{c.definition.Key.Name: true}
	for _, p := range c.definition.Data {
		fields[p.Name] = true
	}
	for _, field := range opts.OrderBy {
		if !fields[field.Property] {
			return nil, vectordata.ErrInvalidFilter
		}
	}
	records, err := c.snapshot(ctx, opts.IncludeVectors)
	if err != nil {
		return nil, err
	}
	out := []vectordata.ModelRecord{}
	for _, r := range records {
		match, err := vectordata.MatchModelFilter(filter, r, c.definition)
		if err != nil {
			return nil, err
		}
		if match {
			out = append(out, r)
		}
	}
	value := func(r vectordata.ModelRecord, name string) any {
		if name == c.definition.Key.Name {
			return r.Key
		}
		return r.Data[name]
	}
	sort.Slice(out, func(i, j int) bool {
		for _, field := range opts.OrderBy {
			a, b := value(out[i], field.Property), value(out[j], field.Property)
			cmp := vectordata.CompareModelValues(a, b)
			if cmp != 0 {
				if a == nil || b == nil {
					return a != nil
				}
				if field.Descending {
					return cmp > 0
				}
				return cmp < 0
			}
		}
		return vectordata.CompareModelValues(out[i].Key, out[j].Key) < 0
	})
	start := min(opts.Skip, len(out))
	return out[start : start+min(top, len(out)-start)], nil
}
func (c *Collection) QueryIter(ctx context.Context, filter vectordata.Filter, top int, opts vectordata.QueryOptions) iter.Seq2[vectordata.ModelRecord, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelRecord, error) {
		return c.Query(ctx, filter, top, opts)
	})
}
func (c *Collection) Count(ctx context.Context, filter vectordata.Filter) (int64, error) {
	if filter == nil {
		c.store.mu.RLock()
		defer c.store.mu.RUnlock()
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		state, err := c.stateLocked()
		if err != nil {
			return 0, err
		}
		return int64(len(state.records)), nil
	}
	if err := vectordata.ValidateModelFilter(filter, c.definition); err != nil {
		return 0, err
	}
	var count int64
	for record, err := range c.searchSnapshot(ctx) {
		if err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		match, err := vectordata.MatchModelFilter(filter, record, c.definition)
		if err != nil {
			return 0, err
		}
		if match {
			count++
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return count, nil
}
func (c *Collection) Delete(ctx context.Context, keys []any) (int64, error) {
	normalized := make([]any, len(keys))
	for i, key := range keys {
		v, err := c.definition.NormalizeKey(key)
		if err != nil {
			return 0, err
		}
		normalized[i] = v
	}
	if len(keys) == 0 {
		return 0, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	s, err := c.stateLocked()
	if err != nil {
		return 0, err
	}
	var count int64
	for _, key := range normalized {
		if _, ok := s.records[key]; ok {
			delete(s.records, key)
			count++
		}
	}
	return count, nil
}
func (c *Collection) Search(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return nil, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return nil, err
	}
	vector, err = vectordata.NormalizeModelVector(vector, p)
	if err != nil {
		return nil, err
	}
	if opts.ProviderOptions != nil {
		return nil, vectordata.ErrUnsupported
	}
	return c.searchRecords(ctx, top, opts, p, func(r vectordata.ModelRecord) (float64, error) {
		return exactsearch.DenseDistance(vector, r.Vectors[p.Name], p.Metric), nil
	})
}
func (c *Collection) searchRecords(ctx context.Context, top int, opts vectordata.ModelSearchOptions, p vectordata.VectorProperty, measure exactsearch.Measure) ([]vectordata.ModelSearchResult, error) {
	return exactsearch.Run(ctx, c.definition, p, top, opts, c.searchSnapshot(ctx), measure)
}

// Stored record values are immutable: writes replace them and public reads clone
// them. Copy headers under the lock, then evaluate caller filters after release.
func (c *Collection) searchSnapshot(ctx context.Context) iter.Seq2[vectordata.ModelRecord, error] {
	return func(yield func(vectordata.ModelRecord, error) bool) {
		records, err := func() ([]vectordata.ModelRecord, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			c.store.mu.RLock()
			defer c.store.mu.RUnlock()
			state, err := c.stateLocked()
			if err != nil {
				return nil, err
			}
			out := make([]vectordata.ModelRecord, 0, len(state.records))
			for _, record := range state.records {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				out = append(out, record)
			}
			return out, nil
		}()
		if err != nil {
			yield(vectordata.ModelRecord{}, err)
			return
		}
		for _, record := range records {
			if !yield(record, nil) {
				return
			}
		}
	}
}
func (c *Collection) SearchIter(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelSearchResult, error) {
		return c.Search(ctx, vector, top, opts)
	})
}
func (c *Collection) SearchText(ctx context.Context, text string, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return c.SearchInput(ctx, text, top, opts)
}
func (c *Collection) SearchInput(ctx context.Context, input any, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return nil, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return nil, err
	}
	if err := vectordata.ValidateModelFilter(opts.Filter, c.definition); err != nil {
		return nil, err
	}
	if opts.ProviderOptions != nil {
		return nil, vectordata.ErrUnsupported
	}
	if err := c.checkSchema(ctx); err != nil {
		return nil, err
	}
	vector, err := vectordata.EmbedInputQuery(ctx, input, p, c.options)
	if err != nil {
		return nil, err
	}
	return c.Search(ctx, vector, top, opts)
}

// stateLocked validates schema identity in the same critical section as record
// access. The caller must hold the store mutex for reading or writing.
func (c *Collection) stateLocked() (*state, error) {
	current, exists := c.store.collections[c.name]
	if !exists {
		return nil, vectordata.ErrCollectionNotFound
	}
	if current.registration != c.registration {
		return nil, fmt.Errorf("%w: collection definition differs", vectordata.ErrSchemaMismatch)
	}
	return current, nil
}

func (c *Collection) checkSchema(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.store.mu.RLock()
	defer c.store.mu.RUnlock()
	_, err := c.stateLocked()
	return err
}
