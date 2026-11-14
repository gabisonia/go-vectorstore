package vectordata

import (
	"context"
	"iter"
)

// CollectionLifecycle is an optional extension for existing Record collections.
type CollectionLifecycle interface {
	Exists(context.Context) (bool, error)
	EnsureExists(context.Context) error
	EnsureDeleted(context.Context) error
}

// RecordQueryer adds batch and filtered reads without breaking implementations
// of the original Collection interface. These reads omit vectors unless requested.
type RecordQueryer interface {
	GetWithOptions(context.Context, string, RetrievalOptions) (Record, error)
	GetMany(context.Context, []string, RetrievalOptions) ([]Record, error)
	Query(context.Context, Filter, int, QueryOptions) ([]Record, error)
	QueryIter(context.Context, Filter, int, QueryOptions) iter.Seq2[Record, error]
}
