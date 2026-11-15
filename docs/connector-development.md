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
- return `vectordata.ErrDimensionMismatch`, `vectordata.ErrInvalidVector`, `vectordata.ErrSchemaMismatch`, and `vectordata.ErrInvalidFilter` where applicable
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

## Model API providers

New providers can implement `vectordata.ModelStore` and `ModelCollection` without
implementing the original Record API. Normalize definitions once, call
`PrepareModelRecords` before writes, preserve native key types and data values,
and run `vectordata/conformance.Run` against the provider. Return `ErrUnsupported`
for unknown provider options; implement `HybridSearcher` only when supported.
Use provider-specific filter translation or `MatchModelFilter` for an in-memory
implementation. The PostgreSQL compiler is in `stores/postgres/filtersql`.
See [model-api.md](model-api.md) for null, projection, key, score and iterator
semantics, and keep integration tests against the real production database.

Providers can additionally implement `vectordata.CapabilityProvider` without
changing `ModelStore`. Return a local `ProviderCapabilities` snapshot with supported
key/generated-key/data types, metrics, physical index methods and data types,
full-text indexing and hybrid search. Return independent slices, describe connector
support rather than deployment health, and distinguish accepted schema hints from
actual indexes. `conformance.Run` exercises advertised features when this optional
interface is present, including native keys/data, generated keys, metrics, indexes
and hybrid search. Providers without metadata continue to run the other contract
checks.

Use `VectorIndexCapabilities` to report supported method/metric/precision tuples
and positive indexed dimension limits; coarse lists do not imply every combination
is valid. Keep discovery local, return an independent slice, and derive validation
and metadata from the same rules. An omitted detailed list remains compatible with
existing providers. The conformance runner creates and validates every advertised
tuple. L1-capable providers also run expected-distance, score, threshold, paging
and text-search checks; hybrid scores retain RRF semantics. PostgreSQL rejects L1
with IVFFlat indexes or tuning before storage/embedding work.

Native `TypeUUID` and `TypeBytes` properties are explicit schema choices; preserve
existing string/JSON inference. `NormalizeModelData` canonicalizes UUID values and
copies byte buffers; `CloneModelRecord` protects mutable read values. Preserve the
difference between NULL and non-nil empty bytes. Filters must canonicalize native
values before binding, while keeping JSON values under JSON semantics. PostgreSQL
does this through `filtersql.FilterSQLConfig.NormalizeColumnValue` without casting
indexed columns to text. `conformance.Run` exercises advertised UUID/byte support,
including aliases, typed mapping, nulls, sorting, filters and buffer ownership.

`TypeInt64Array`, `TypeFloat64Array`, `TypeBoolArray`, `TypeUUIDArray` and
`TypeTimeArray` are explicit native array
choices; preserve existing JSON inference. `DataType.ArrayElementType` identifies
native arrays, while `NormalizeModelData` validates/copies whole values and
`NormalizeModelArrayElement` canonicalizes membership operands. Preserve NULL vs
non-nil empty arrays, reject NULL elements and non-finite floats, and copy mutable
read values. PostgreSQL preserves dimensions while scanning so unsupported shapes
are rejected rather than flattened. Use trusted `ArrayElementTypes` casts and
`NormalizeArrayElement` in `filtersql.FilterSQLConfig` to type membership operands
without casting indexed columns. UUID elements use canonical strings; timestamp
elements use UTC with microsecond precision and UTC years 1–9999. Apply these
normalizations to writes, reads and filter operands. Scalar timestamps keep their
existing behavior. PostgreSQL casts UUID arrays to `text[]` only in read
projections, preserving array dimensions and leaving indexed predicates native.
Typed decoding must support text-mapped UUIDs and defined `time.Time` types without
depending on JSON methods. The shared suite checks all five types, null
semantics, aliases, filters, search, atomic failures and legacy JSON round trips.

`TypeDateOnly`/`TypeTimeOnly` and their array counterparts are opt-in calendar
values. Normalize them with `NormalizeModelData` and `NormalizeModelArrayElement`:
canonical ISO strings, Gregorian years 1–9999, clock times below 24:00:00 with
microsecond truncation, no timezone or timestamp coercion. Dynamic arrays return
`[]string`; typed fields use string aliases or the `DateOnly`/`TimeOnly` helpers.
Scalar fields support ordered comparisons and B-tree indexes; arrays use GIN.
PostgreSQL decodes native date/time values before normalization, preserving array
dimensions and avoiding session formatting dependencies. Reject out-of-range SQL
dates, infinities and 24:00:00 on read. Preserve existing inference and timestamp
behavior; shared calendar and native-array tests cover filters, search and typed
round trips, with PostgreSQL-specific migration and index-plan checks.

`TypeDecimal`/`TypeDecimalArray` preserve exact values with a 96-bit coefficient,
separate sign and scale 0–28. Use `NormalizeModelData` for scalar/whole-array
values and `NormalizeModelArrayElement` for scalar membership operands. Dynamic
values are canonical `Decimal`/`[]Decimal`; strings, `json.Number` and integers
are accepted, while floating-point inputs and values requiring rounding fail.
Compare decimals numerically, without float64 conversion or lexical string order.
Typed fields support string types and aliases. Native storage remains opt-in;
legacy float/string/JSON mappings must not change implicitly. PostgreSQL uses
unconstrained `numeric`/`numeric[]`, B-tree/GIN indexes, and text read projections
that retain array dimensions and reject non-finite/out-of-range SQL values.
Shared tests cover exact fractions, numeric ordering, filters, search, typed
mapping, ownership, atomic failures and legacy JSON precision.

Advertise supported `VectorPrecisions` explicitly. Dense Go vectors stay float32;
`NormalizeModelVector` validates and rounds float16 values. It returns a new slice
for float16 and the original slice for float32, so copy float32 inputs before
retaining them. Use it for both stored vectors and search queries. The capability
conformance tests verify mixed precision, embedding generation, query rounding,
text/hybrid search and atomic rejection of invalid values. Persist precision and
validate matching column/index types; classify changes as manual conversions.

Advertise `VectorKinds` for dense/binary/sparse support. An empty schema kind stays dense
and is omitted from registrations for compatibility. Binary properties use
`BinaryVector` with packed bytes and exact dimensions, `ModelRecord.BinaryVectors`,
and Hamming distance. Use `NormalizeBinaryVector` and copy bytes on reads; reject
wrong map kinds, bit lengths and nonzero padding before writes/embedding work.
Implement optional `BinarySearcher`; hybrid-capable providers can also implement
`BinaryHybridSearcher`. Float/text query methods must reject binary properties
without calling a float embedder. Binary index capability entries specify kind,
Hamming distance and bit limits, with empty precision. Shared tests cover mixed
models, typed mapping, ownership, scores, filters, paging, projection, atomicity
and iterators. PostgreSQL uses packed `pgtype.Bits` values and `bit(n)` parameters;
read projections use `varbit` to preserve exact length and a stable result type.
Validate visible operators/opclasses and classify new fields/conversions as
manual backfill work; compatible index additions remain additive.

Sparse properties use `VectorSparse`, `SparseVector` and
`ModelRecord.SparseVectors`, with optional `SparseSearcher` and
`SparseHybridSearcher`. Use `NormalizeSparseVector` to validate dimensions,
coordinate ordering/uniqueness, finite values, cosine norms and index nonzero
limits. Remove explicit zeros, copy both coordinate buffers, and keep all
operations proportional to coordinate counts. Go indices are zero-based;
PostgreSQL text indices are one-based. Sparse float32 supports the four dense
metrics, with HNSW only. Capability entries report both `MaxDimensions` and
`MaxNonZero`. Reject invalid maps and sparse inputs before embedding/writes;
text queries must not call a float embedder for a sparse property. Preserve
projection/iterator/typed semantics across all vector maps. PostgreSQL validates
visible sparse types/operators/opclasses and checks existing nonzero counts
before planning an index addition; manual backfill and stale reviews remain
explicit. Shared tests cover mixed models, all metrics and billion-dimensional
values without dense allocation.

`vectordata.MigrationPlanner` is a separate optional capability for model schema
planning. Advertise `MigrationPlanning` only when it is implemented. Planning must
be read-only and return provider SQL/arguments for review; classify manual work,
data removal, backfills and embedding changes explicitly. Keep registry
finalization separate and do not infer support for execution from planning.
Advertise `MigrationApplication` only with `vectordata.MigrationApplier` and
planning support. Application must reject stale/edited reviews and non-additive
changes, regenerate SQL from locked live state, validate the resulting model and
commit storage/index/registration changes atomically. Preserve provider errors and
support context cancellation. The conformance suite exercises optional-property
application and preserved records for each advertised native key type.
See [migrations.md](migrations.md) for the PostgreSQL implementation and limits.

For store-wide embedding defaults, resolve `ModelCollectionOptions.WithDefaults`
when constructing collection handles. Runtime settings must not enter persisted
definitions. Use `PrepareModelRecords` before storage writes so bounded requests,
output validation and cancellation complete before mutation. `EmbedQuery` resolves
property/collection embedders for text search. Run
`conformance.RunEmbeddingConfiguration(t, factory)` in addition to `Run`; its
factory receives embedding defaults and must return an isolated store configured
with them. It verifies override precedence, batching, text/hybrid queries and
atomic insert/upsert behavior after embedding errors or cancellation.


## Optional vectors and array set filters

Advertise `OptionalVectors` only when every supported vector kind can be absent.
Use `HasModelVector` to distinguish missing sparse/binary entries from valid zero
vectors. Query vectors still undergo full validation. Reads preserve absence and
all searches exclude records missing the selected vector before ranking/paging;
hybrid candidates follow the same rule. `PrepareModelRecords` validates optional
values and resolves optional embedding sources before writes.

Translate ContainsAnyFilter/ContainsAllFilter for native arrays with SQL NULL
semantics and the existing Contains element normalization. JSON paths retain
whole-element equality, including nested arrays and JSON null. See the model
guide for empty-operand rules. The shared contract exercises these filters,
optional vectors, and nullable search/hybrid behavior. SQLite demonstrates a
persistent provider reusing the exact-search engine; it adds file persistence and
concurrent-handle/transaction tests alongside the shared suite.
