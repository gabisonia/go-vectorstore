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

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type state struct {
	definition vectordata.CollectionDefinition
	records    map[any]vectordata.ModelRecord
	next       int64
}
type Store struct {
	mu          sync.RWMutex
	collections map[string]*state
	options     vectordata.ModelCollectionOptions
}

func New() *Store { return &Store{collections: map[string]*state{}} }

// StoreOptions configures runtime defaults for model collections.
type StoreOptions struct {
	Embedder vectordata.Embedder
	// EmbeddingBatchSize bounds embedding requests; zero leaves them unbounded.
	EmbeddingBatchSize int
}

// NewWithOptions creates a store with default embedding settings. New retains its
// original behavior. Collection and property embedders can override this default.
func NewWithOptions(opts StoreOptions) (*Store, error) {
	defaults, err := (vectordata.ModelCollectionOptions{Embedder: opts.Embedder, EmbeddingBatchSize: opts.EmbeddingBatchSize}).WithDefaults(vectordata.ModelCollectionOptions{})
	if err != nil {
		return nil, err
	}
	return &Store{collections: map[string]*state{}, options: defaults}, nil
}

var _ vectordata.ModelStore = (*Store)(nil)

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
	return &Collection{store: s, name: name, definition: definition, options: opts}, nil
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
	store      *Store
	name       string
	definition vectordata.CollectionDefinition
	options    vectordata.ModelCollectionOptions
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
		if definitionJSON(current.definition) != definitionJSON(c.definition) {
			return fmt.Errorf("%w: collection definition differs", vectordata.ErrSchemaMismatch)
		}
		return nil
	}
	if c.store.collections == nil {
		c.store.collections = map[string]*state{}
	}
	c.store.collections[c.name] = &state{definition: c.definition.Clone(), records: map[any]vectordata.ModelRecord{}, next: 1}
	return nil
}
func (c *Collection) Insert(ctx context.Context, records []vectordata.ModelRecord) ([]any, error) {
	return c.write(ctx, records, false)
}
func (c *Collection) Upsert(ctx context.Context, records []vectordata.ModelRecord) ([]any, error) {
	return c.write(ctx, records, true)
}
func (c *Collection) write(ctx context.Context, records []vectordata.ModelRecord, upsert bool) ([]any, error) {
	prepared, err := vectordata.PrepareModelRecords(ctx, c.definition, c.options, records)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return []any{}, nil
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	s, exists := c.store.collections[c.name]
	if !exists {
		return nil, vectordata.ErrCollectionNotFound
	}
	staged := make(map[any]vectordata.ModelRecord, len(s.records)+len(prepared))
	for key, r := range s.records {
		staged[key] = r
	}
	keys := make([]any, len(prepared))
	for i, r := range prepared {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		generated := r.Key == nil
		if generated {
			r.Key = s.next
			s.next++
		}
		if _, exists := staged[r.Key]; exists && (!upsert || generated) {
			return nil, fmt.Errorf("%w: duplicate key", vectordata.ErrInvalidRecord)
		}
		staged[r.Key] = r
		keys[i] = r.Key
	}
	s.records = staged
	return keys, nil
}
func (c *Collection) snapshot(ctx context.Context, vectors bool) ([]vectordata.ModelRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.store.mu.RLock()
	defer c.store.mu.RUnlock()
	s, exists := c.store.collections[c.name]
	if !exists {
		return nil, vectordata.ErrCollectionNotFound
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
	s, exists := c.store.collections[c.name]
	if !exists {
		return vectordata.ModelRecord{}, vectordata.ErrCollectionNotFound
	}
	r, exists := s.records[normalized]
	if !exists {
		return vectordata.ModelRecord{}, vectordata.ErrNotFound
	}
	return vectordata.CloneModelRecord(r, c.definition, opts.IncludeVectors), nil
}
func (c *Collection) GetMany(ctx context.Context, keys []any, opts vectordata.RetrievalOptions) ([]vectordata.ModelRecord, error) {
	values := make([]any, len(keys))
	for i, key := range keys {
		value, err := c.definition.NormalizeKey(key)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	if len(values) == 0 {
		return []vectordata.ModelRecord{}, ctx.Err()
	}
	return c.Query(ctx, vectordata.In(vectordata.Column(c.definition.Key.Name), values...), len(values), vectordata.QueryOptions{IncludeVectors: opts.IncludeVectors})
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
	return func(yield func(vectordata.ModelRecord, error) bool) {
		records, err := c.Query(ctx, filter, top, opts)
		if err != nil {
			yield(vectordata.ModelRecord{}, err)
			return
		}
		for _, r := range records {
			if err := ctx.Err(); err != nil {
				yield(vectordata.ModelRecord{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
	}
}
func (c *Collection) Count(ctx context.Context, filter vectordata.Filter) (int64, error) {
	records, err := c.Query(ctx, filter, int(^uint(0)>>1), vectordata.QueryOptions{})
	return int64(len(records)), err
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
	s, exists := c.store.collections[c.name]
	if !exists {
		return 0, vectordata.ErrCollectionNotFound
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
func distance(a, b []float32, metric vectordata.DistanceMetric) float64 {
	if metric == vectordata.DistanceL1 {
		var sum float64
		for i, x := range a {
			sum += math.Abs(float64(x) - float64(b[i]))
		}
		return sum
	}
	var dot, aa, bb, sum float64
	for i, x := range a {
		xx, yy := float64(x), float64(b[i])
		dot += xx * yy
		aa += xx * xx
		bb += yy * yy
		delta := xx - yy
		sum += delta * delta
	}
	switch metric {
	case vectordata.DistanceL2:
		return math.Sqrt(sum)
	case vectordata.DistanceInnerProduct:
		return -dot
	default:
		cosine := dot / (math.Sqrt(aa) * math.Sqrt(bb))
		return 1 - math.Max(-1, math.Min(1, cosine))
	}
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
	return c.searchRecords(ctx, top, opts, p.Metric, func(r vectordata.ModelRecord) float64 { return distance(vector, r.Vectors[p.Name], p.Metric) })
}
func (c *Collection) searchRecords(ctx context.Context, top int, opts vectordata.ModelSearchOptions, metric vectordata.DistanceMetric, measure func(vectordata.ModelRecord) float64) ([]vectordata.ModelSearchResult, error) {
	records, err := c.Query(ctx, opts.Filter, int(^uint(0)>>1), vectordata.QueryOptions{IncludeVectors: true})
	if err != nil {
		return nil, err
	}
	out := []vectordata.ModelSearchResult{}
	for _, r := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d := measure(r)
		score := vectordata.ScoreFromDistance(metric, d)
		if opts.DistanceThreshold != nil && d > *opts.DistanceThreshold || opts.ScoreThreshold != nil && score < *opts.ScoreThreshold {
			continue
		}
		if !opts.IncludeVectors {
			r.Vectors = nil
			r.BinaryVectors = nil
			r.SparseVectors = nil
		}
		out = append(out, vectordata.ModelSearchResult{Record: r, Distance: d, Score: score, ScoreKind: vectordata.ScoreSimilarity})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	start := min(opts.Skip, len(out))
	return out[start : start+min(top, len(out)-start)], nil
}
func (c *Collection) SearchIter(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return func(yield func(vectordata.ModelSearchResult, error) bool) {
		results, err := c.Search(ctx, vector, top, opts)
		if err != nil {
			yield(vectordata.ModelSearchResult{}, err)
			return
		}
		for _, r := range results {
			if err := ctx.Err(); err != nil {
				yield(vectordata.ModelSearchResult{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
	}
}
func (c *Collection) SearchText(ctx context.Context, text string, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
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
	vector, err := vectordata.EmbedQuery(ctx, text, p, c.options)
	if err != nil {
		return nil, err
	}
	return c.Search(ctx, vector, top, opts)
}
