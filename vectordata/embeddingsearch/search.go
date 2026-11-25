// Package embeddingsearch composes query embedding with typed dense search.
package embeddingsearch

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/internal/iteration"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/filterast"
)

// Searcher embeds text using the selected model vector's embedding settings.
type Searcher[T any] struct {
	dense   vectordata.VectorSearcher[T]
	def     vectordata.CollectionDefinition
	options vectordata.ModelCollectionOptions
}

// New binds a dense searcher to its logical model and embedding configuration.
func New[T any](dense vectordata.VectorSearcher[T], definition vectordata.CollectionDefinition, options vectordata.ModelCollectionOptions) (*Searcher[T], error) {
	if filterast.IsNilValue(dense) {
		return nil, fmt.Errorf("%w: dense searcher required", vectordata.ErrSchemaMismatch)
	}
	def, err := definition.Normalize()
	if err != nil {
		return nil, err
	}
	if _, err := options.WithDefaults(vectordata.ModelCollectionOptions{}); err != nil {
		return nil, err
	}
	return &Searcher[T]{dense, def, options}, nil
}

// SearchTextIter embeds once per enumeration and delegates to dense search.
// Provider-specific options require a provider-specific text search entry point.
func (s *Searcher[T]) SearchTextIter(ctx context.Context, text string, top int, opts vectordata.ModelSearchOptions) iter.Seq2[vectordata.TypedModelSearchResult[T], error] {
	return func(yield func(vectordata.TypedModelSearchResult[T], error) bool) {
		var zero vectordata.TypedModelSearchResult[T]
		if err := ctx.Err(); err != nil {
			yield(zero, err)
			return
		}
		if err := vectordata.ValidateModelSearchOptions(top, opts); err != nil {
			yield(zero, err)
			return
		}
		if opts.ProviderOptions != nil {
			yield(zero, vectordata.ErrUnsupported)
			return
		}
		p, err := s.def.Vector(opts.VectorProperty)
		if err != nil {
			yield(zero, err)
			return
		}
		frozen, err := exactsearch.FreezeSearchOptions(opts, s.def)
		if err != nil {
			yield(zero, err)
			return
		}
		frozen.VectorProperty = p.Name
		vector, err := vectordata.EmbedQuery(ctx, text, p, s.options)
		if err = errors.Join(err, ctx.Err()); err != nil {
			yield(zero, err)
			return
		}
		iteration.WithContext(ctx, s.dense.SearchIter(ctx, vector, top, frozen))(yield)
	}
}
