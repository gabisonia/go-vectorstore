# Connector Development Guide

This guide explains how to add a provider under `stores/<backend>`. Compare the
[provider matrix](parity.md) and [store implementation](stores-implementation.md)
before choosing storage and optional capabilities.

## 1) Scope and contract

A model provider implements `vectordata.ModelStore` (including CollectionManager)
and `vectordata.ModelCollection`. PostgreSQL, SQLite, Redis and memory share this contract.
Optional interfaces cover binary/sparse search, hybrid search, capabilities and
migrations. Implement only the optional workflows the backend supports.

The original `vectordata.VectorStore` and `Collection` contracts are separate.
PostgreSQL implements them as well; a model-only provider does not need to.
They use CollectionSpec, Record, SearchByVector and EnsureIndexes rather than
model definitions, named vectors and model query options.

Both contracts must preserve context cancellation, errors.Is/As, vector validation,
score semantics and caller ownership of mutable values.

## 2) Recommended layout

Typical components are store/lifecycle, collection writes and reads, query/search,
filter translation, and provider tests. Separate schema, codec and migration files
when the provider needs them. Constructor and option names follow the provider:
PostgreSQL uses NewVectorStore, SQLite uses Open/Close, and memory uses New.

## 3) Store implementation checklist

1. Normalize store defaults, including runtime embedding options.
2. Implement GetModelCollection as a validated handle constructor without storage I/O.
3. Implement EnsureModelCollection and EnsureExists with idempotent creation and
   strict definition/storage validation. Keep migrations explicit.
4. Implement ListCollectionNames, CollectionExists and idempotent DeleteCollection.
5. Define resource ownership and Close behavior for pools/files owned by the store.
6. Return a local, caller-owned capability snapshot when implementing CapabilityProvider.

For the original Record API, implement EnsureCollection and Collection as well;
validate CollectionSpec and its schema mode separately from model definitions.

## 4) Collection implementation checklist

1. Normalize definitions once. Use PrepareModelRecords to copy/validate the whole
   input and generate requested embeddings before write transactions.
2. Implement atomic Insert/Upsert batches, native keys and supported generated keys.
   Return keys in input order without mutating caller records.
3. Implement Get/GetMany, Delete and Count. Get returns ErrNotFound; GetMany omits
   missing keys. Use ErrCollectionNotFound for missing collection errors.
4. Implement filtered Query/QueryIter with scalar ordering, deterministic key
   tie-breaks, Skip/top and optional vector projection.
5. Implement Search/SearchIter/SearchText with named vector selection, valid query
   vectors, filters, thresholds, scores and paging. Unknown ProviderOptions must
   return ErrUnsupported. Preserve absence of optional vectors and exclude records
   missing the selected property before ranking/paging.
6. Implement supported binary/sparse/hybrid search interfaces separately. Hybrid
   scores use ScoreRRF, while ordinary vector results use ScoreSimilarity.
7. Define iterator materialization/streaming behavior and release resources on
   completion, cancellation, errors and early exit. Copy mutable returned values.

For Record collections, validate ID/Vector/Metadata, honor SearchOptions projection
and thresholds, and implement EnsureIndexes or return explicit unsupported-option
errors. Record collection modes do not apply to model migrations.

## 5) Filter strategy

Translate the portable AST to bound backend queries or evaluate it in Go with
ValidateModelFilter and MatchModelFilter. PostgreSQL translation is in
`stores/postgres/filtersql`; the compiler entry point in vectordata is a deprecated
compatibility shim. Values must remain bound parameters.

Preserve native normalization, SQL NULL versus JSON null, missing JSON paths and
UNKNOWN under negation. Support the common filter nodes and report invalid
structures/values as ErrInvalidFilter even when the collection is empty. Native
ContainsAny/ContainsAll need overlap/containment semantics; JSON paths compare
whole array elements. See [filter semantics](model-api.md#filtering).

## 6) Schema handling

Model definitions describe key/data/vector types, nullability, storage mappings
and index hints. Ensure validates these expectations; it does not implicitly
convert registered models. Implement MigrationPlanner/MigrationApplier only with
the documented review, drift and transaction guarantees. A provider may reject
unsupported index hints or accept them for portability, but capability metadata
must describe physical indexes actually provided.

For the original Record API, CollectionSpec.Mode selects EnsureStrict or
EnsureAutoMigrate. The latter permits supported additions of missing optional
Record columns; it does not authorize arbitrary model migrations or conversions.

## 7) Testing requirements

Run `vectordata/conformance.Run` with a fresh provider/namespace for each test and
clean up resources through t.Cleanup. The suite exercises CRUD, keys, filters,
projections, ordering, scores, thresholds, cancellation and advertised optional
features. Run RunEmbeddingConfiguration for store/collection/property defaults.
Add provider-specific schema, driver, transaction and persistence tests.

Embedded stores can run file-reopen/concurrent-handle tests with normal unit tests.
Server providers should also have tests using `//go:build integration`, disposable
Testcontainers fixtures and an optional test DSN override. Memory conformance does
not replace exercising the real database.

The [Redis connector](redis.md) demonstrates a standalone server provider with
atomic snapshots: one final SET publishes an entire batch and generated sequence.
Do not assume Lua or MULTI provides rollback after a runtime command error.
Disable client automatic write retries and retry only conflicts that prove no
mutation occurred. Redis conformance runs against a real server with an isolated
namespace, including uncertain replies, concurrency and corruption checks.

```sh
go test -race ./...
go test -race -tags=integration -timeout=5m ./stores/postgres
```

Adapt the second command to the provider and its integration requirements.

## 8) Repository wiring

Update the root README, docs index, architecture/store design, provider matrix,
model capabilities, runnable samples and performance/verification commands.
Add dependencies to go.mod/go.sum and the required checks to CI. Add Docker
Aspire resources or Make targets only if the new provider needs them.

## 9) Contract checks

Use compile-time assertions in the implementation:

```go
var _ vectordata.ModelStore = (*Store)(nil)
var _ vectordata.ModelCollection = (*Collection)(nil)
```

Wire the shared suite in provider tests. Here newTestStore is a test helper that
opens the actual provider with a fresh namespace/file and registers cleanup:

```go
func TestModelContract(t *testing.T) {
    conformance.Run(t, func(t *testing.T) vectordata.ModelStore {
        return newTestStore(t)
    })
}
```

Import testing, vectordata and vectordata/conformance. Follow the existing
SQLite/memory tests for embedded providers and PostgreSQL integration tests for
server-backed storage.

## Model normalization and optional capabilities

Integer `KeyTypes` describe canonical storage: `KeyInt64` and `KeyUint64` also
accept narrower Go keys through typed wrappers. Providers continue to return
canonical int64/uint64 values. Typed writes preflight keys against K's range before
calling the provider; keep batch atomicity and input ownership intact. Generated
integer bindings require 64-bit keys. Shared conformance exercises signed/unsigned
widths and named types across CRUD, query/search, filters and iterators.

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
PostgreSQL scalar fields support ordered comparisons and B-tree indexes; arrays
use GIN.
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
PostgreSQL text indices are one-based. Sparse float32 supports the five dense
metrics, with HNSW only. Capability entries report both `MaxDimensions` and
`MaxNonZero`. Reject invalid maps and sparse inputs before embedding/writes;
text queries must not call a float embedder for a sparse property. Preserve
projection/iterator/typed semantics across all vector maps. PostgreSQL validates
visible sparse types/operators/opclasses and checks existing nonzero counts
before planning an index addition; manual backfill and stale reviews remain
explicit. Shared tests cover mixed models, all metrics and billion-dimensional
values without dense allocation.

`DistanceL2Squared` returns the sum of squared component differences, with score
`1 / (1 + distance)` and thresholds in squared units. Preserve existing L2 units.
Advertise the metric and each supported index tuple; shared conformance covers
dense float32/float16, sparse, thresholds, text queries, projection and iterators.
PostgreSQL uses native L2 operators/classes for index ordering and squares only
reported distances and threshold expressions. Preserve that distinction in
ordinary and hybrid plans, including relaxed IVFFlat ordering. A metric change
still requires manual review even when operator classes match. SQLite persists
the new metric in format 1; no vector representation changes are needed.

Advertise `HybridKeywordTerms` only when `HybridSearchOptions.KeywordTerms` works
through every advertised hybrid slice/iterator variant. PostgreSQL combines plain
queries with OR between entries and AND within entries, binding the list as data.
Reject empty lists, invalid/blank entries and conflicting positional keywords
before embedding or storage access. Preserve nil-list string compatibility,
caller ownership, enumeration snapshots and existing candidate/RRF semantics.
Shared conformance exercises every supported vector/input form and list snapshots
across embedding callbacks; PostgreSQL plan checks cover full-text index use.

Implement `StoreMetadataProvider` and `CollectionMetadataProvider` to expose
configured identity through `StoreMetadata`/`CollectionMetadata`. Advertise
`Capabilities().Metadata` only when both are available. Keep discovery local,
stable and independent of schema validation or connection state; return value
snapshots without driver clients, DSNs or credentials. Empty identity fields mean
unavailable. Typed wrappers forward the optional interface, with a collection-name
fallback for existing providers. Shared conformance checks discovery before
creation and after deletion without creating storage as a side effect.

When wrapping failures in `OperationError`, populate its embedded `StoreMetadata`
from the configured store. Preserve the original cause with `Unwrap`, including
native driver errors and context cancellation. Leave error category decisions and
message formatting intact. Do not query the database to assemble error identity;
diagnostics must also work with closed or failed connections. Error wrapping is
optional and is not implied by the `Metadata` discovery capability. Shared
metadata conformance checks supplied error metadata against collection identity;
existing providers may leave the new error metadata empty.

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

For custom inputs, resolve `InputEmbedder` alongside `Embedder` at each level;
`WithDefaults` rejects ambiguous options and handles cross-kind overrides. Pass
`ModelRecord.EmbeddingInputs` to `PrepareModelRecords` and store only its prepared
output. Advertise `InputEmbedding` only when the collection implements optional
`InputSearcher`; use `EmbedInputQuery` after validating search options and filters.
Implement `InputHybridSearcher` when hybrid input search is available. The embedding
configuration suite also exercises byte/native/custom inputs, direct string
fields, transient-input ownership, migration-plan stripping and atomic failures
when this capability is advertised. Its factory must propagate `InputEmbedder`.


## Native keys

For `KeyUint64`, canonical keys are uint64 and filter operands must retain integer
precision. PostgreSQL uses numeric(20,0) with native uint64 binding/scanning; SQLite
uses exact JSON numbers rather than float64. Do not truncate to int64. Generated
UUID keys are application-generated random v4 values; generated insert requests
must still reject collisions under Upsert. Only generated int64 keys use integer
sequences/identity columns. The capability suite exercises key bounds, typed
aliases, CRUD/filter/search behavior, generated UUIDs and migration compatibility.

## Read iterator extensions

Keep ModelStore/ModelCollection and existing optional search interfaces compatible.
Implement `CollectionNameIterator`, `ModelBatchIterator`, `TextSearchIterator` and
advertise `ReadIterators`. `InputEmbedding` additionally requires
`InputSearchIterator`; `HybridSearch` adds `HybridSearchIterator` and supported
input/binary/sparse hybrid iterator interfaces. Package helpers supply buffered
fallbacks for existing slice-only providers.

Defer reads, embedding and decoding until enumeration. Stop after the first error
or a false yield; check context cancellation before work, between records and at
exhaustion. Close cursors/transactions on every exit. Do not retry after yielding
results. Typed codecs must decode only consumed records. Snapshot providers must
release locks/connections before caller code, especially SQLite's single connection.
Document buffering and cross-batch consistency separately from iterator API support.
The shared capability suite covers these contracts; PostgreSQL also tests early
exit before a late malformed row and single-connection cleanup/settings rollback.

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
