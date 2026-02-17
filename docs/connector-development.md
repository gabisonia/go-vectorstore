# Connector Development Guide

This guide explains how to add a new backend connector under `stores/<backend>`.

## 1) Scope and Contract

Every connector must implement the shared interfaces in `vectordata`:

- `vectordata.VectorStore`
- `vectordata.Collection`

The connector must preserve shared behavior:

- validate collection specs (`name`, `dimension`, `metric`, `mode`)
- enforce vector dimension on write and search
- return `vectordata.ErrNotFound` for missing records
- return `vectordata.ErrDimensionMismatch`, `vectordata.ErrSchemaMismatch`, and `vectordata.ErrInvalidFilter` where applicable
- compute score via `vectordata.ScoreFromDistance`

## 2) Recommended Layout

Create a new directory:

- `stores/<backend>/doc.go`
- `stores/<backend>/store.go`
- `stores/<backend>/collection.go`
- `stores/<backend>/schema.go`
- `stores/<backend>/helpers.go`
- `stores/<backend>/<backend>_integration_test.go`

Add extra files only when needed (for example filter compiler/evaluator files).

## 3) Store Implementation Checklist

Implement a store type that owns connection/resources and options:

1. Add `StoreOptions` and `DefaultStoreOptions()`.
2. Add `NewVectorStore(...)` constructor with option normalization.
3. Implement `EnsureCollection(ctx, spec)`:
   - normalize and validate spec
   - ensure schema/table/metadata structures
   - create or validate collection storage
4. Implement `Collection(name, dimension, metric)` as a lightweight handle constructor.

## 4) Collection Implementation Checklist

Implement collection operations with strict validation:

1. `Insert` and `Upsert`
   - validate ID and vector dimension
   - normalize nil metadata to empty object
   - use batching/chunking for bulk writes
2. `Get` and `Delete`
   - return `ErrNotFound` from `Get` when missing
3. `Count`
   - support nil filter and filter predicates
4. `SearchByVector`
   - validate `topK > 0`
   - validate query vector dimension
   - apply filter (SQL pushdown or in-process evaluation)
   - apply optional threshold
   - honor projection (`Metadata`, `Content`, `Vector`)
   - return results ordered by best match first
   - compute `Score` from `Distance`
5. `EnsureIndexes`
   - create backend-specific indexes when supported
   - return explicit error if unsupported options are requested

## 5) Filter Strategy

Choose one strategy:

- SQL pushdown: compile `vectordata.Filter` to parameterized backend SQL
- In-process: load records and evaluate filter AST in Go

Requirements:

- never interpolate raw values into SQL
- return `vectordata.ErrInvalidFilter` for invalid AST/input

## 6) Schema Safety Modes

Respect `CollectionSpec.Mode`:

- `EnsureStrict`: fail on mismatches
- `EnsureAutoMigrate`: add/fix optional schema parts where possible

When mode is unset, use connector defaults (`StrictByDefault` behavior).

## 7) Testing Requirements

Add both unit and integration coverage.

Unit tests:

- spec validation
- schema mismatch behavior
- filter behavior (happy path + invalid filters)
- dimension mismatch and error mapping
- projection and threshold behavior in search

Integration tests:

- put in `stores/<backend>/<backend>_integration_test.go`
- use `//go:build integration`
- start backend with Testcontainers when DSN env var is absent
- allow DSN override via `<BACKEND>_TEST_DSN`

Run commands:

```bash
go test ./...
go test -tags=integration ./stores/<backend>
```

## 8) Repository Wiring

When adding a connector, also update:

1. `README.md`:
   - scope
   - requirements
   - project layout
   - integration test notes
2. `docs/architecture.md` and `docs/stores-implementation.md`
3. `docs/README.md`
4. `Makefile` integration targets (if per-backend targets are used)
5. `docker-compose.yml` only if manual local backend compose is required
6. `go.mod` and `go.sum` dependencies

## 9) Minimal Skeleton

```go
package mybackend

import (
	"context"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type StoreOptions struct{}

func DefaultStoreOptions() StoreOptions { return StoreOptions{} }

type VectorStore struct{}

func NewVectorStore(opts StoreOptions) (*VectorStore, error) {
	return &VectorStore{}, nil
}

func (s *VectorStore) EnsureCollection(ctx context.Context, spec vectordata.CollectionSpec) (vectordata.Collection, error) {
	return nil, nil
}

func (s *VectorStore) Collection(name string, dimension int, metric vectordata.DistanceMetric) vectordata.Collection {
	return nil
}
```
