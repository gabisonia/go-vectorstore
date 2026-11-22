package redis

import (
	"context"
	"fmt"
	"iter"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Disable automatic embeddings while reconstructing stored records. The client
// connection has already been released before any normalization/callback runs.
func (c *Collection) readDefinition() vectordata.CollectionDefinition {
	d := c.definition.Clone()
	for i := range d.Vectors {
		d.Vectors[i].SourceProperty = ""
		d.Vectors[i].Embedder = nil
		d.Vectors[i].InputEmbedder = nil
	}
	return d
}
func (c *Collection) decode(ctx context.Context, state snapshot, tokens []string) ([]vectordata.ModelRecord, error) {
	records := make([]vectordata.ModelRecord, 0)
	read := func(t string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		payload, ok := state.Records[t]
		if !ok {
			return nil
		}
		r, err := modelcodec.Decode(payload, c.definition)
		if err != nil {
			return err
		}
		if token(r.Key) != t {
			return fmt.Errorf("%w: Redis record key differs from map key", vectordata.ErrInvalidRecord)
		}
		records = append(records, r)
		return nil
	}
	if tokens == nil {
		for t := range state.Records {
			if err := read(t); err != nil {
				return nil, err
			}
		}
	} else {
		seen := map[string]bool{}
		for _, t := range tokens {
			if seen[t] {
				continue
			}
			seen[t] = true
			if err := read(t); err != nil {
				return nil, err
			}
		}
	}
	return vectordata.PrepareModelRecords(ctx, c.readDefinition(), vectordata.ModelCollectionOptions{}, records)
}
func (c *Collection) Get(ctx context.Context, key any, opts vectordata.RetrievalOptions) (record vectordata.ModelRecord, err error) {
	defer func() { err = c.operation(ctx, "get", err) }()
	tokens, err := c.normalizeKeys([]any{key})
	if err != nil {
		return record, err
	}
	state, _, err := c.load(ctx)
	if err != nil {
		return record, err
	}
	rows, err := c.decode(ctx, state, tokens)
	if err != nil {
		return record, err
	}
	if len(rows) == 0 {
		return record, vectordata.ErrNotFound
	}
	return vectordata.CloneModelRecord(rows[0], c.definition, opts.IncludeVectors), nil
}
func (c *Collection) GetMany(ctx context.Context, keys []any, opts vectordata.RetrievalOptions) (records []vectordata.ModelRecord, err error) {
	defer func() { err = c.operation(ctx, "get_many", err) }()
	tokens, err := c.normalizeKeys(keys)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return []vectordata.ModelRecord{}, ctx.Err()
	}
	state, _, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	records, err = c.decode(ctx, state, tokens)
	if err != nil {
		return nil, err
	}
	for i, r := range records {
		records[i] = vectordata.CloneModelRecord(r, c.definition, opts.IncludeVectors)
	}
	return records, nil
}
func (c *Collection) snapshotRecords(ctx context.Context) ([]vectordata.ModelRecord, error) {
	state, _, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	return c.decode(ctx, state, nil)
}

func (c *Collection) engine(ctx context.Context) (*memory.Collection, error) {
	records, err := c.snapshotRecords(ctx)
	if err != nil {
		return nil, err
	}
	base, err := memory.New().EnsureModelCollection(ctx, c.name, c.readDefinition(), vectordata.ModelCollectionOptions{})
	if err != nil {
		return nil, err
	}
	if _, err = base.Insert(ctx, records); err != nil {
		return nil, err
	}
	return base.(*memory.Collection), nil
}
func (c *Collection) Query(ctx context.Context, filter vectordata.Filter, top int, opts vectordata.QueryOptions) (records []vectordata.ModelRecord, err error) {
	defer func() { err = c.operation(ctx, "query", err) }()
	if err = vectordata.ValidateModelQuery(c.definition, top, opts); err != nil {
		return nil, err
	}
	if err = vectordata.ValidateModelFilter(filter, c.definition); err != nil {
		return nil, err
	}
	if c.store.nativeIndexes && filter != nil {
		rows, _, used, nativeErr := c.nativeQuery(ctx, filter, top, opts, false)
		if nativeErr != nil || used {
			return rows, nativeErr
		}
	}
	engine, err := c.engine(ctx)
	if err != nil {
		return nil, err
	}
	return engine.Query(ctx, filter, top, opts)
}
func (c *Collection) Count(ctx context.Context, filter vectordata.Filter) (count int64, err error) {
	defer func() { err = c.operation(ctx, "count", err) }()
	if err = vectordata.ValidateModelFilter(filter, c.definition); err != nil {
		return 0, err
	}
	if c.store.nativeIndexes {
		_, n, used, nativeErr := c.nativeQuery(ctx, filter, 0, vectordata.QueryOptions{}, true)
		if nativeErr != nil || used {
			return n, nativeErr
		}
	}
	if filter == nil {
		state, _, err := c.load(ctx)
		if err != nil {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return int64(len(state.Records)), nil
	}
	engine, err := c.engine(ctx)
	if err != nil {
		return 0, err
	}
	return engine.Count(ctx, filter)
}
func (c *Collection) validateSearch(top int, opts vectordata.ModelSearchOptions) (vectordata.VectorProperty, error) {
	p, err := c.definition.Vector(opts.VectorProperty)
	if err != nil {
		return p, err
	}
	if err = vectordata.ValidateModelSearchOptions(top, opts); err != nil {
		return p, err
	}
	if err = vectordata.ValidateModelFilter(opts.Filter, c.definition); err != nil {
		return p, err
	}
	if _, err = c.searchTuning(opts); err != nil {
		return p, err
	}
	return p, nil
}
func (c *Collection) Search(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) (results []vectordata.ModelSearchResult, err error) {
	defer func() { err = c.operation(ctx, "search", err) }()
	p, err := c.validateSearch(top, opts)
	if err != nil {
		return nil, err
	}
	vector, err = vectordata.NormalizeModelVector(vector, p)
	if err != nil {
		return nil, err
	}
	tuning, err := c.searchTuning(opts)
	if err != nil {
		return nil, err
	}
	if c.store.nativeIndexes {
		rows, used, nativeErr := c.nativeSearch(ctx, vector, p, top, opts, tuning)
		if nativeErr != nil || used {
			return rows, nativeErr
		}
	}
	opts.ProviderOptions = nil
	records, err := c.snapshotRecords(ctx)
	if err != nil {
		return nil, err
	}
	return exactsearch.DenseSnapshot(ctx, c.definition, p, vector, top, opts, records)
}
func (c *Collection) SearchText(ctx context.Context, text string, top int, opts vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return c.SearchInput(ctx, text, top, opts)
}
func (c *Collection) SearchInput(ctx context.Context, input any, top int, opts vectordata.ModelSearchOptions) (results []vectordata.ModelSearchResult, err error) {
	defer func() { err = c.operation(ctx, "search_input", err) }()
	p, err := c.validateSearch(top, opts)
	if err != nil {
		return nil, err
	}
	opts, err = exactsearch.FreezeSearchOptions(opts, c.definition)
	if err != nil {
		return nil, err
	}
	if c.store.nativeIndexes {
		_, _, _, err = c.nativeManifest(ctx)
	} else {
		_, _, err = c.load(ctx)
	}
	if err != nil {
		return nil, err
	}
	vector, err := vectordata.EmbedInputQuery(ctx, input, p, c.options)
	if err != nil {
		return nil, err
	}
	return c.Search(ctx, vector, top, opts)
}
func (s *Store) ListCollectionNamesIter(ctx context.Context) iter.Seq2[string, error] {
	return iteration.FromSlice(ctx, func() ([]string, error) { return s.ListCollectionNames(ctx) })
}
func (c *Collection) GetManyIter(ctx context.Context, keys []any, opts vectordata.RetrievalOptions) iter.Seq2[vectordata.ModelRecord, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelRecord, error) { return c.GetMany(ctx, keys, opts) })
}
func (c *Collection) QueryIter(ctx context.Context, filter vectordata.Filter, top int, opts vectordata.QueryOptions) iter.Seq2[vectordata.ModelRecord, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelRecord, error) { return c.Query(ctx, filter, top, opts) })
}
func (c *Collection) SearchIter(ctx context.Context, vector []float32, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelSearchResult, error) { return c.Search(ctx, vector, top, opts) })
}
func (c *Collection) SearchTextIter(ctx context.Context, text string, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return c.SearchInputIter(ctx, text, top, opts)
}
func (c *Collection) SearchInputIter(ctx context.Context, input any, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return iteration.FromSlice(ctx, func() ([]vectordata.ModelSearchResult, error) { return c.SearchInput(ctx, input, top, opts) })
}

var _ vectordata.InputSearcher = (*Collection)(nil)
var _ vectordata.CollectionNameIterator = (*Store)(nil)
var _ vectordata.ModelBatchIterator = (*Collection)(nil)
var _ vectordata.TextSearchIterator = (*Collection)(nil)
var _ vectordata.InputSearchIterator = (*Collection)(nil)
