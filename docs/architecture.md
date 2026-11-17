# Architecture and Request Flow

This page explains the internal design of `go-vectorstore` and how requests move through the library.

## 1) Layering

The shared core and provider implementations are:

- `vectordata`: backend-agnostic contracts and primitives
- `stores/postgres`: PostgreSQL + pgvector implementation
- `stores/sqlite`: durable embedded model storage with exact snapshot search
- `stores/memory`: transient exact-search model implementation
- `stores/redis`: standalone atomic snapshots and dense Go exact search

Providers share model contracts, portable filter evaluation, and a conformance suite.

## 2) Core Data Model

The model API uses CollectionDefinition to describe a native key, data properties
and named vectors. ModelRecord holds Key, Data, Vectors, BinaryVectors and
SparseVectors. Providers advertise supported vector kinds through capabilities.
TypedModelCollection[K,T] maps
application structs through tags or a custom ModelCodec. See the
[model guide](model-api.md) for native types, optional vectors and embeddings.

The original PostgreSQL Record API uses `vectordata.Record`:

- `ID` (`string`): primary key
- `Vector` (`[]float32`): embedding values
- `Metadata` (`map[string]any`): structured JSON metadata
- `Content` (`*string`): optional text payload

The Postgres backend decodes metadata numbers as `json.Number`, including nested
objects and arrays, to preserve integer and decimal precision. Use `Int64()` or
`Float64()` when an application needs a Go numeric value; `Float64()` can lose
precision. This replaces the previous `float64` decoding behavior.

Search returns `vectordata.SearchResult`:

- `Record`: matched item
- `Distance`: backend-computed distance value
- `Score`: normalized score derived from distance

Score normalization:

- cosine: `1 - distance`
- L2/L1/Hamming: `1 / (1 + distance)` (Hamming applies to binary model vectors)
- inner product: `-distance`

## 3) Public API Shape

Model interfaces are `ModelStore`, `CollectionManager` and `ModelCollection`.
They provide lifecycle management, Insert/Upsert, Get/GetMany, Query/QueryIter,
Delete/Count, Search/SearchIter and SearchText. Binary/sparse search, hybrid search,
capability discovery and migrations use optional interfaces.

The original Record API uses `VectorStore` and `Collection`, with these operations:

- `EnsureCollection`
- `Insert`, `Upsert`
- `Get`, `Delete`, `Count`
- `SearchByVector`
- `EnsureIndexes`

Storage operations take `context.Context`; handle constructors and local metadata
accessors do not access storage.

## 4) Runtime Defaults and Configuration

`postgres.StoreOptions` defaults (`postgres.DefaultStoreOptions()`):

- `Schema`: `public`
- `EnsureExtension`: `true`
- `StrictByDefault`: `true`

Record API collection defaults:

- Metric defaults to cosine when omitted
- Ensure mode defaults to:
  - `EnsureStrict` if `StrictByDefault=true`
  - `EnsureAutoMigrate` if `StrictByDefault=false`

PostgreSQL Record API operational defaults:

- Writes are encoded and executed one batch at a time (`maxRowsPerStatement=500`) in a single transaction
- Requests with at least 1,024 vector values use binary parameters; smaller writes use text
- A binary write holds its pool lease through transaction cleanup and restores the caller's type map before release (`vector_write.go`)
- Search default projection includes `Metadata` and `Content`, but not `Vector`

## 5) Request Flow

### Postgres: Ensure collection

`PostgresVectorStore.EnsureCollection`:

1. Validates collection spec (`name`, `dimension`, `metric`, `mode`)
2. Ensures `vector` extension (if enabled) and schema
3. Checks whether the table exists
4. Creates the table or validates existing schema
5. Returns a `PostgresCollection` handle

Default table shape:

```sql
id text primary key,
vector vector(n) not null,
metadata jsonb not null default '{}'::jsonb,
content text
```

### Postgres: Insert / Upsert

Bulk writes are chunked and executed as parameterized SQL:

- `Insert`: plain `INSERT`
- `Upsert`: `INSERT ... ON CONFLICT (id) DO UPDATE`

Each record is validated before sending:

- non-empty `ID`
- vector dimension match
- finite vector elements
- nonzero cosine vectors (zero vectors remain valid for L2, L1 and inner product)
- metadata JSON serialization

### Postgres: Search

`SearchByVector` builds a query plan, then executes it:

1. Validates `topK > 0`
2. Validates query vector dimension, finite elements, and nonzero norm for cosine
3. Chooses operator by metric:
   - cosine: `<=>`
   - l2: `<->`
   - l1: `<+>`
   - inner product: `<#>`
4. Builds `distance` expression (`"vector" <op> $1::vector`)
5. Applies optional filter SQL
6. Applies optional backend distance threshold (`distance <= threshold`; lower is stricter)
7. Orders by `distance ASC`, limits by `topK`
8. Scans rows into `SearchResult`

The pgvector operators are documented in the [pgvector README](https://github.com/pgvector/pgvector#querying).

## 6) Filter System (AST -> SQL)

Filters are represented as an AST in `vectordata`:

- `Eq`, `Ne`, `In`, `Gt`, `Gte`, `Lt`, `Lte`, `Exists`
- `Contains`, `ContainsAny`, `ContainsAll`, `And`, `Or`, `Not`

Fields can target:

- Record API columns (`Column("id")`, `Column("content")`)
- model key/data properties (`Column("Category")`) and JSON properties (`DataPath("Details", "rating")`)
- metadata JSON paths (`Metadata("category")`, `Metadata("a", "b")`); each segment is preserved exactly, including empty and whitespace-only keys

`CompileFilterSQL` converts AST to:

- SQL predicate fragment
- parameter list

Behavior details:

- SQL injection safety is preserved by binding values as query args
- metadata `Eq`/`In` compares JSONB values (`::jsonb`), so value types matter
- column `Eq` with a nil value matches SQL NULL; metadata `Eq` with nil matches JSON null, excluding missing paths
- metadata `Gt`/`Lt` uses numeric comparison when the input is numeric, otherwise text comparison
- numeric comparisons use PostgreSQL `numeric` and accept Go numeric values and `json.Number` without converting through `float64`
- numeric-looking metadata strings are compared numerically; malformed or out-of-range strings do not match numeric comparisons
- column filters are whitelist-based (`id`, `content` for Record collections; registered key/data fields for models)
- native array set filters use PostgreSQL `&&`/`@>` with normalized operands; JSON paths retain whole-element matching
- memory and Redis evaluate model filters through the shared in-process evaluator;
  SQLite pushes supported scalar queries into SQL and uses that evaluator otherwise

JSON path extraction behavior comes from PostgreSQL [JSON/JSONB functions and operators](https://www.postgresql.org/docs/current/functions-json.html).

## 7) Schema Safety Modes

`CollectionSpec.Mode` controls ensure behavior:

- `EnsureStrict`:
  - fails if expected columns/type/dimension mismatch
- `EnsureAutoMigrate`:
  - can add missing optional columns (`metadata`, `content`)

Dimension is always validated against `vector(n)` and must match.
`id`, `vector`, and existing `metadata` columns must be `NOT NULL`; `content`
must allow NULL. Both ensure modes reject incompatible nullability before adding
optional columns, without altering existing constraints.
The primary key must be non-deferrable and contain only `id`, as required by upserts.
Existing column types and dimensions are checked before auto-migration adds optional columns.

## 8) Index Management

`EnsureIndexes` supports:

- Vector index:
  - HNSW (default method)
  - IVFFlat (optional)
- Metadata index:
  - GIN on `metadata` JSONB
  - optional `jsonb_path_ops`

Ensure verifies existing indexes by table, method, column, operator class,
validity, predicate, and requested vector build options. Conflicts return
`ErrSchemaMismatch`; indexes are never silently replaced. An explicitly requested
index metric must match the collection metric.
Generated names over PostgreSQL's 63-byte limit use a shortened prefix and hash;
explicit index names must fit within that limit.

Metadata `Eq` and `In` filters add JSONB containment predicates that can use either
GIN operator class, alongside exact extraction comparisons. The exact checks keep
object and array equality semantics. Paths that may traverse array indexes, and
filters beneath `Not`, retain extraction-only predicates to preserve their
matching behavior. Range comparisons and `Exists` still require other indexing
strategies; the whole-column GIN index does not accelerate them.

Defaults when index options are omitted:

- vector index name: `idx_<collection>_vector_<method>`
- metadata index name: `idx_<collection>_metadata_gin`
- HNSW: `m=16`, `ef_construction=64`
- IVFFlat: `lists=100`

Metric-specific operator classes are selected automatically:

- cosine -> `vector_cosine_ops`
- l2 -> `vector_l2_ops`
- inner product -> `vector_ip_ops`

For vector indexing details:

- HNSW: [original paper](https://arxiv.org/abs/1603.09320) and [overview](https://en.wikipedia.org/wiki/Hierarchical_navigable_small_world)
- IVFFlat: [FAISS index reference](https://github.com/facebookresearch/faiss/wiki/Faiss-indexes) and [pgvector IVFFlat docs](https://github.com/pgvector/pgvector#ivfflat)

For metadata indexing details:

- GIN index basics: [PostgreSQL GIN indexes](https://www.postgresql.org/docs/current/gin.html)
- JSONB operators and indexing context: [PostgreSQL JSON functions and operators](https://www.postgresql.org/docs/current/functions-json.html)

## 9) Typed Extension

The model API provides `StructCodec[T]`, custom `ModelCodec[T]` and
`TypedModelCollection[K,T]` for native-key application models across all providers.

The original Record API also supports:

- `vectordata.Codec[T]`
- `vectordata.TypedCollection[T]`

This wrapper retains the original Record storage shape.

## 10) Error Model

Common exported errors:

- `ErrNotFound`
- `ErrDimensionMismatch`
- `ErrInvalidVector`
- `ErrSchemaMismatch`
- `ErrInvalidFilter`

Errors are wrapped with context so callers can use `errors.Is(...)` against base error types.
Vector write validation also matches `ErrInvalidRecord`, while preserving
`ErrInvalidVector` or `ErrDimensionMismatch` as appropriate.

## 11) Sample App Flows

### `samples/model-store` and `samples/sqlite-store`

The model sample demonstrates tagged models, generated keys, store-wide embedding
defaults and array filters. It supports memory, SQLite and PostgreSQL. The SQLite
sample retains typed records, queries a draft with a missing vector, searches the
record with a vector, and reopens the database to verify persistence. Both run
without an API key. See [samples](../samples/README.md).

### `samples/semantic-search`

1. Build embeddings for 3 fake articles via OpenAI embeddings API
2. Ensure a Postgres vector collection
3. Upsert article records
4. Ensure vector + metadata indexes
5. Embed user query and run semantic search
6. Print ranked top matches with score and distance

### `samples/ragrimosa`

1. Build embeddings for manually chunked Lacrimosa story records
2. Ensure a Postgres vector collection
3. Upsert chunk records and ensure indexes
4. Embed user prompt and retrieve nearest chunks from DB
5. Send retrieved chunks as context to OpenAI chat completions
6. Print the grounded answer

## 12) Current Scope and Limits

Current scope:

- PostgreSQL + pgvector, embedded SQLite and standalone Redis snapshot backends
- original Record API: one vector per collection
- model API: native keys, data properties, and multiple named vectors
- in-memory model provider for development and shared contract tests
- metadata filtering through a focused AST

Future extension points:

- additional backends under `stores/`
- richer filter operators
- optional reranking strategies

## 13) References

- pgvector project docs: https://github.com/pgvector/pgvector
- PostgreSQL docs: https://www.postgresql.org/docs/current/index.html

## Model-driven API

The original Record API is complemented by `ModelStore` and `ModelCollection`.
`CollectionDefinition` describes a native key, data fields, named vector fields,
and their storage/index mappings. `StructCodec[T]` resolves tagged or explicit
mappings once; `TypedModelCollection[K,T]` exposes the full model API.

`stores/postgres` maps definitions to native columns and registers them in
`__vectorstore_models`. Creation validates schemas and indexes transactionally;
concurrent schema/registry initialization and per-collection creation use advisory
locks. `stores/memory` implements the same model contracts with exact search and
atomic in-memory writes. Shared provider tests live in `vectordata/conformance`.

Portable filter nodes live in `vectordata/filterast`; PostgreSQL compilation is
in `stores/postgres/filtersql`. The original compiler entry point remains a
compatibility shim. Embedding generation is optional and batched per property;
PostgreSQL hybrid search combines full-text and vector candidates with RRF.
See [model-api.md](model-api.md) for the contract and current feature scope.


SQLite persists collection registrations, canonical records, and generated-key
sequences in a database file. Parameterized SQL, foreign-key cascades and immediate
write transactions make batch changes atomic across handles. WAL and FULL
synchronization are configured on each connection. GetMany performs indexed key
lookups within one deferred read snapshot, and unfiltered Count uses SQL counting
without decoding payloads. Key filters use the primary index; supported scalar
queries/counts run in SQL. Other query/count combinations load a snapshot, release
the connection, then use the memory engine. Exact search streams a deferred read
snapshot through the shared `internal/exactsearch` top-K selector, retaining only
`top + Skip` candidates. Memory uses that selector over a shallow snapshot of
immutable records. Both copy only the returned search page and release resources
before application iteration. Runtime embedders are cleared during stored reads.
Native values are decoded by declared type, with a separate SQL NULL marker for JSON properties. See [sqlite.md](sqlite.md) for workload limits.

The default Redis layout stores one versioned collection envelope per Redis string. Reads obtain a
consistent snapshot with one GET. Writes prepare/validate records before publishing
through a compare-and-swap script with one final SET, covering data and generated
keys atomically. Explicit conflicts may retry; uncertain results never replay.
Canonical record JSON is shared with SQLite through `internal/modelcodec` without
changing SQLite format 1. Redis supports dense float32/float16 and evaluates
portable filters/exact search through the memory engine after releasing the client
connection. Full-snapshot transfers, a default 16 MiB encoded limit and Redis's
configured persistence/eviction policy define its initial scope. See [redis.md](redis.md).

Opt-in Redis format 2 stages immutable per-record HASH generations under a unique
TTL lease, validates completion, removes staging TTLs and publishes one manifest
SET. An atomic read checks the manifest and fetches its payloads together. Old
generations can then be retired without racing a reader; interrupted cleanup is
reclaimable through manifest-guarded pruning. Existing format-1 collections remain
readable and require explicit storage conversion. Hash scalar and binary vector
fields support optional generation-scoped Redis Search indexes. Native mode builds
the index and verifies readiness before publishing; reads verify manifest/schema
and query the index atomically. Scalar filters and HNSW candidates reduce payload
transfer, with Go sorting/remeasurement and whole-snapshot fallback. Each write
still rebuilds a full generation and index. FLAT exhaustively retrieves matching
vectors for Go scoring/selection, preserving ordering through FLOAT32 roundoff;
it reduces payload transfer only when filters or optional vectors exclude rows.
Native full-text hints add TEXT fields over existing raw string projections.
Index metadata version 2 preserves version-1 scalar/vector-only reads until a
rebuild or native write; the storage format and registrations are unchanged.
Public keyword-query/hybrid APIs remain unsupported.
