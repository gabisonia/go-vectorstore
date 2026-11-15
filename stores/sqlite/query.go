package sqlite

import (
	"context"
	"iter"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// One LEFT JOIN reads both metadata and records from the same SQLite snapshot,
// including when the collection is empty. The connection is released before
// user code or query evaluation runs.
func (c *Collection) read(ctx context.Context, key *string) ([]vectordata.ModelRecord, error) {
	statement := `SELECT c.definition,c.format,r.payload FROM __vectorstore_collections c LEFT JOIN __vectorstore_records r ON r.collection=c.name`
	args := []any{}
	if key != nil {
		statement += ` AND r.key=?`
		args = append(args, *key)
	}
	statement += ` WHERE c.name=?`
	args = append(args, c.name)
	rows, err := c.store.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := false
	records := []vectordata.ModelRecord{}
	for rows.Next() {
		found = true
		var registration string
		var format int
		var payload []byte
		if err := rows.Scan(&registration, &format, &payload); err != nil {
			return nil, err
		}
		if err := c.checkDefinition(registration, format); err != nil {
			return nil, err
		}
		if payload == nil {
			continue
		}
		record, err := decodeRecord(payload, c.definition)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, vectordata.ErrCollectionNotFound
	}
	return records, nil
}
func (c *Collection) engine(ctx context.Context) (*memory.Collection, error) {
	records, err := c.read(ctx, nil)
	if err != nil {
		return nil, err
	}
	d := c.definition.Clone()
	// Replaying already embedded records must never call a user embedder, even
	// when an optional vector is absent and the source property is present.
	for i := range d.Vectors {
		d.Vectors[i].SourceProperty = ""
		d.Vectors[i].Embedder = nil
	}
	base, err := memory.New().EnsureModelCollection(ctx, c.name, d, vectordata.ModelCollectionOptions{})
	if err != nil {
		return nil, err
	}
	if _, err = base.Insert(ctx, records); err != nil {
		return nil, err
	}
	return base.(*memory.Collection), nil
}
func (c *Collection) Get(ctx context.Context, key any, opts vectordata.RetrievalOptions) (record vectordata.ModelRecord, err error) {
	defer func() { err = c.operation("get", err) }()
	key, err = c.definition.NormalizeKey(key)
	if err != nil {
		return record, err
	}
	token := keyToken(key)
	records, err := c.read(ctx, &token)
	if err != nil {
		return record, err
	}
	if len(records) == 0 {
		return record, vectordata.ErrNotFound
	}
	return vectordata.CloneModelRecord(records[0], c.definition, opts.IncludeVectors), nil
}
func (c *Collection) GetMany(ctx context.Context, keys []any, opts vectordata.RetrievalOptions) ([]vectordata.ModelRecord, error) {
	values := make([]any, len(keys))
	for i, key := range keys {
		v, err := c.definition.NormalizeKey(key)
		if err != nil {
			return nil, c.operation("get_many", err)
		}
		values[i] = v
	}
	if len(values) == 0 {
		return []vectordata.ModelRecord{}, ctx.Err()
	}
	return c.Query(ctx, vectordata.In(vectordata.Column(c.definition.Key.Name), values...), len(values), vectordata.QueryOptions{IncludeVectors: opts.IncludeVectors})
}
func (c *Collection) Query(ctx context.Context, filter vectordata.Filter, top int, opts vectordata.QueryOptions) (records []vectordata.ModelRecord, err error) {
	defer func() { err = c.operation("query", err) }()
	if err = vectordata.ValidateModelQuery(c.definition, top, opts); err != nil {
		return nil, err
	}
	if err = vectordata.ValidateModelFilter(filter, c.definition); err != nil {
		return nil, err
	}
	engine, err := c.engine(ctx)
	if err != nil {
		return nil, err
	}
	return engine.Query(ctx, filter, top, opts)
}
func sequence[T any](ctx context.Context, run func() ([]T, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		items, err := run()
		if err != nil {
			yield(zero, err)
			return
		}
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				yield(zero, err)
				return
			}
			if !yield(item, nil) {
				return
			}
		}
	}
}
func (c *Collection) QueryIter(ctx context.Context, f vectordata.Filter, top int, o vectordata.QueryOptions) iter.Seq2[vectordata.ModelRecord, error] {
	return sequence(ctx, func() ([]vectordata.ModelRecord, error) { return c.Query(ctx, f, top, o) })
}
func (c *Collection) Count(ctx context.Context, filter vectordata.Filter) (count int64, err error) {
	defer func() { err = c.operation("count", err) }()
	if err = vectordata.ValidateModelFilter(filter, c.definition); err != nil {
		return 0, err
	}
	engine, err := c.engine(ctx)
	if err != nil {
		return 0, err
	}
	return engine.Count(ctx, filter)
}
func (c *Collection) Search(ctx context.Context, v []float32, top int, o vectordata.ModelSearchOptions) (hits []vectordata.ModelSearchResult, err error) {
	defer func() { err = c.operation("search", err) }()
	p, err := c.validateSearch(top, o)
	if err != nil {
		return nil, err
	}
	v, err = vectordata.NormalizeModelVector(v, p)
	if err != nil {
		return nil, err
	}
	engine, err := c.engine(ctx)
	if err != nil {
		return nil, err
	}
	return engine.Search(ctx, v, top, o)
}
func (c *Collection) validateSearch(top int, o vectordata.ModelSearchOptions) (vectordata.VectorProperty, error) {
	p, err := c.definition.Vector(o.VectorProperty)
	if err != nil {
		return p, err
	}
	if err := vectordata.ValidateModelSearchOptions(top, o); err != nil {
		return p, err
	}
	if err := vectordata.ValidateModelFilter(o.Filter, c.definition); err != nil {
		return p, err
	}
	if o.ProviderOptions != nil {
		return p, vectordata.ErrUnsupported
	}
	return p, nil
}
func (c *Collection) SearchIter(ctx context.Context, v []float32, top int, o vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return sequence(ctx, func() ([]vectordata.ModelSearchResult, error) { return c.Search(ctx, v, top, o) })
}
func (c *Collection) SearchText(ctx context.Context, text string, top int, o vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	p, err := c.validateSearch(top, o)
	if err != nil {
		return nil, c.operation("search_text", err)
	}
	v, err := vectordata.EmbedQuery(ctx, text, p, c.options)
	if err != nil {
		return nil, c.operation("embed_query", err)
	}
	return c.Search(ctx, v, top, o)
}
func (c *Collection) SearchBinary(ctx context.Context, v vectordata.BinaryVector, top int, o vectordata.ModelSearchOptions) (hits []vectordata.ModelSearchResult, err error) {
	defer func() { err = c.operation("search_binary", err) }()
	p, err := c.validateSearch(top, o)
	if err != nil {
		return nil, err
	}
	v, err = vectordata.NormalizeBinaryVector(v, p)
	if err != nil {
		return nil, err
	}
	engine, err := c.engine(ctx)
	if err != nil {
		return nil, err
	}
	return engine.SearchBinary(ctx, v, top, o)
}
func (c *Collection) SearchBinaryIter(ctx context.Context, v vectordata.BinaryVector, top int, o vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return sequence(ctx, func() ([]vectordata.ModelSearchResult, error) { return c.SearchBinary(ctx, v, top, o) })
}
func (c *Collection) SearchSparse(ctx context.Context, v vectordata.SparseVector, top int, o vectordata.ModelSearchOptions) (hits []vectordata.ModelSearchResult, err error) {
	defer func() { err = c.operation("search_sparse", err) }()
	p, err := c.validateSearch(top, o)
	if err != nil {
		return nil, err
	}
	v, err = vectordata.NormalizeSparseVector(v, p)
	if err != nil {
		return nil, err
	}
	engine, err := c.engine(ctx)
	if err != nil {
		return nil, err
	}
	return engine.SearchSparse(ctx, v, top, o)
}
func (c *Collection) SearchSparseIter(ctx context.Context, v vectordata.SparseVector, top int, o vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return sequence(ctx, func() ([]vectordata.ModelSearchResult, error) { return c.SearchSparse(ctx, v, top, o) })
}
