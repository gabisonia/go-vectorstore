// Package sqlitevec enables opt-in sqlite-vec execution over the SQLite provider.
// It links the pinned pure-Go extension, which registers its SQL functions on
// subsequent modernc SQLite connections in this process. Opening this adapter
// selects packed vector projections and exact scalar-L1 candidate execution;
// other metrics and unsupported query shapes retain the SQLite provider's paths.
package sqlitevec

import (
	"context"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	_ "modernc.org/sqlite/vec"
)

// StoreOptions shares SQLite configuration. Open selects sqlite-vec execution
// when VectorExecution is unset and implies VectorProjections. Existing
// collections still require an explicit RebuildVectorProjections.
type StoreOptions = sqlite.StoreOptions

type Store = sqlite.Store
type Collection = sqlite.Collection

// Open opens the shared SQLite provider with verified sqlite-vec execution.
// Close releases its connection. Canonical format, metadata and capabilities
// remain those of sqlite; extension presence does not advertise a vector index.
func Open(ctx context.Context, path string, opts StoreOptions) (*Store, error) {
	if opts.VectorExecution == sqlite.VectorExecutionGo {
		opts.VectorExecution = sqlite.VectorExecutionSQLiteVec
	}
	return sqlite.Open(ctx, path, opts)
}
