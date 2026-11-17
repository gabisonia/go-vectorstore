# Stores Implementation Design

This document explains how `go-vectorstore` store backends are implemented, how they are structured, and what behavior is important when you use or extend them.

## 1) Design Goals

- Keep provider-neutral model and Record contracts in `vectordata`
- Isolate backend specifics inside `stores/<backend>`
- Enforce schema and vector-dimension correctness at runtime
- Keep write/read/search behavior consistent across implementations
- Allow adding future store implementations without changing caller-facing contracts

## 2) Layering and Boundaries

The shared core and provider packages are:

- `vectordata`: contracts, records, model codecs, errors, scores and portable filter evaluation
- `vectordata/filterast`: portable filter node types
- `stores/postgres/filtersql`: PostgreSQL SQL compiler; the core compiler entry point is a compatibility shim
- `stores/postgres`: native model columns plus the original Record API
- `stores/sqlite`: durable model storage with exact snapshot queries/search
- `stores/memory`: transient model storage with exact search
- `stores/redis`: atomic collection snapshots with Go dense exact search
- `internal/modelcodec`: canonical JSON record encoding shared by SQLite and Redis

All built-in providers implement ModelStore and ModelCollection. Only PostgreSQL
also implements the original VectorStore/Collection contracts. New model providers
do not need to implement the Record API. See the [provider matrix](parity.md).

## 3) Shared Core Contracts (`vectordata`)

The model API consists of CollectionDefinition, ModelRecord, ModelStore,
ModelCollection, typed codecs and search/query options. PrepareModelRecords
normalizes/copies writes and resolves automatic dense embeddings before a provider
opens its write transaction. CloneModelRecord protects mutable reads. Vectors may
be required or optional; searches skip records missing the selected property.

The original Record API has these types:

- `CollectionSpec`: `{Name, Dimension, Metric, Mode}`
- `Record`: `{ID, Vector, Metadata, Content}`
- `SearchOptions`: `{Filter, Projection, Threshold}`
- `IndexOptions`: vector and metadata index options
- `Collection` and `VectorStore` interfaces

Shared runtime behavior:

- Metric defaults to cosine if omitted
- `Projection` defaults to metadata+content (vector excluded)
- Score is normalized from distance via `ScoreFromDistance`
- Common errors:
  - `ErrNotFound`
  - `ErrDimensionMismatch`
  - `ErrSchemaMismatch`
  - `ErrInvalidFilter`

## 4) Postgres Store (`stores/postgres`)

### 4.1 Main Components

- `PostgresVectorStore` (`store.go`)
  - Owns connection pool and global options (`Schema`, `EnsureExtension`, `StrictByDefault`)
  - Validates and normalizes collection specs
  - Ensures base schema and collection table lifecycle
- `PostgresCollection` (`collection.go`)
  - Implements operational methods for a specific collection
  - Builds SQL for writes/search/count
  - Handles projection, filter compilation, scoring, and index creation
- Schema utilities (`schema.go`)
  - Creates schema/table
  - Validates required columns/types/primary key
  - Reads vector dimension from catalog (`vector(n)`)
  - Performs auto-migrate for missing optional columns in auto-migrate mode
- Helpers (`helpers.go`)
  - Identifier quoting, metric/operator mapping, vector literal encoding/decoding, metadata JSON normalization

### 4.2 EnsureCollection Flow

`EnsureCollection` does:

1. Validate/normalize `CollectionSpec`
2. Ensure `vector` extension (if enabled) and SQL schema
3. Check table existence
4. Create table or validate existing schema
5. Return `PostgresCollection` handle

Default table shape:

```sql
id text primary key,
vector vector(n) not null,
metadata jsonb not null default '{}'::jsonb,
content text
```

### 4.3 Write Path (Insert / Upsert)

- Records are validated:
  - `ID` must be non-empty
  - vector length must match collection dimension
  - metadata must be JSON-serializable
- Writes are chunked (`maxRowsPerStatement = 500`)
- Insert uses single batch `INSERT`
- Upsert uses `INSERT ... ON CONFLICT (id) DO UPDATE`

### 4.4 Read / Count / Search Path

- `Get` selects row by `id`, parses pgvector text representation and metadata JSON
- `Delete` uses `WHERE id = ANY($1)`
- `Count` compiles filter AST to SQL (`CompileFilterSQL`) and executes `COUNT(*)`

`SearchByVector` pipeline:

1. Validate `topK`, query dimensions, finite values and cosine norm
2. Resolve metric operator (`<=>`, `<->`, `<#>`, `<+>`)
3. Build distance expression and dynamic projection columns
4. Compile optional filter AST into SQL + bind args
5. Apply optional backend distance threshold (`distance <= threshold`; lower is stricter)
6. Order by ascending distance and limit by `topK`
7. Scan rows and map distance to score

### 4.5 Index Management

`EnsureIndexes` can create:

- Vector index (HNSW or IVFFlat)
  - metric-specific opclass: `vector_cosine_ops`, `vector_l2_ops`, `vector_ip_ops`, `vector_l1_ops` (L1 requires HNSW)
  - defaults:
    - HNSW: `m=16`, `ef_construction=64`
    - IVFFlat: `lists=100`
- Metadata GIN index
  - optional `jsonb_path_ops`

Metadata Eq/In can add GIN-compatible containment prefilters while preserving
exact JSONB extraction comparisons. Range/Exists predicates do not generally use
the whole-column GIN index. Model native arrays use GIN-compatible membership,
overlap and containment predicates.

## 5) Filter System and Execution Model

Filter AST supports:

- `Eq`, `Ne`, `In`, `Gt`, `Gte`, `Lt`, `Lte`, `Exists`
- `Contains`, `ContainsAny`, `ContainsAll`, `And`, `Or`, `Not`
- Fields: Record columns/metadata paths or model key/data fields and DataPath JSON paths

Execution strategy by backend:

- PostgreSQL: compile AST to parameterized SQL via `stores/postgres/filtersql`
- Memory/Redis models and SQLite fallback queries: evaluate the shared AST against collection snapshots

Important behavior:

- Invalid AST structures return `ErrInvalidFilter`
- Numeric comparisons are numeric when values are numeric; otherwise textual comparison is used
- Preserve SQL NULL versus JSON null, missing paths, and UNKNOWN under negation; see [filter semantics](model-api.md#filtering)

## 6) Schema Safety Modes

`CollectionSpec.Mode` controls schema handling:

- `EnsureStrict`: fail on mismatch/missing required expectations
- `EnsureAutoMigrate`: add missing optional columns (`metadata`, `content`) where supported

Mode default comes from backend options:

- `StrictByDefault=true` -> strict mode when spec mode is unset
- `StrictByDefault=false` -> auto-migrate mode when spec mode is unset

## 7) Invariants

These rules describe PostgreSQL Record collections:

- Collection name must be non-empty
- Dimension must be greater than zero
- Dense metric must be supported (`cosine`, `l2`, `inner_product`, `l1`)
- Record ID must be non-empty for writes
- Query and record vectors must match collection dimension
- Nil metadata is normalized to empty object
- `Get` returns `ErrNotFound` on missing ID

## 8) Extending with a New Backend

To add a new store backend, follow the same contract shape:

1. Implement ModelStore and ModelCollection, normalizing definitions and preserving native types.
2. Prepare/validate complete batches before committing atomic writes.
3. Preserve missing optional vectors, dimensions, precision, score and projection semantics.
4. Translate filters or reuse the portable evaluator with SQL/JSON NULL behavior.
5. Implement and advertise only supported optional capabilities.
6. Run shared conformance plus real persistence/transaction and provider-specific tests.

Implement VectorStore/Collection as well when adding support for the original
Record API; its CollectionSpec and strict/auto-migrate modes are separate.

For a practical checklist and repository wiring steps, see [`connector-development.md`](connector-development.md).

## Model API implementation

Model collections use a separate native schema defined by
`vectordata.CollectionDefinition`. They support typed keys, data properties,
named vectors, batched embeddings, projected and streaming retrieval, and
PostgreSQL keyword/vector hybrid search. PostgreSQL model tables use native key/data/vector columns. SQLite stores
canonical records and definitions in internal provider tables; memory stores
normalized records in process. The original implementation
and interfaces remain supported.

The portable AST is in `vectordata/filterast`; PostgreSQL compilation is in
`stores/postgres/filtersql`. Memory, SQLite and Redis provide exact search. All
providers run `vectordata/conformance.Run`. SQLite adds file-reopen, concurrent
handle, rollback and precision tests; PostgreSQL integration tests cover the
driver, native schemas, indexes, migrations and transactions. See
[model-api.md](model-api.md) for details and current limits.


## SQLite storage boundary

SQLite owns a database/sql pool with one connection and configures WAL, FULL
synchronization and foreign keys. Context-aware retries wait up to five seconds
for transaction acquisition or a side-effect-free read. Write statements and
commits are not replayed. Immediate write transactions
commit records and generated-key sequences together. Reads check registration and
load records with one consistent SQL snapshot. Get selects a single key directly;
GetMany uses indexed, bounded key batches in a deferred read transaction.
Unfiltered Count uses a schema-checked SQL count without decoding payloads.
Query and filtered Count replay the whole collection through the memory engine.
Exact search instead streams and validates payloads in a deferred read transaction
through `internal/exactsearch`, shared with memory. That selector keeps a max-heap
of at most top + Skip candidates, orders by distance/native key, and clones only
the returned page. Filter operands are normalized before acquiring SQL resources,
so caller codecs run outside the connection. Memory scans a shallow snapshot of
immutable records after releasing its mutex. SQLite clears runtime embedding
sources during stored reads. All SQL resources close before application iteration.

Query/filtered Count snapshot replay costs O(collection size) memory and scan work.
Exact search still scans all records; SQLite still allocates to decode/normalize
every payload. Memory allocates O(collection size) shallow record headers, while
candidate retention in both providers grows with top + Skip. Index hints are
accepted for portability; SQLite advertises no physical property/vector indexes,
full-text/hybrid search or migrations. See [SQLite storage and limits](sqlite.md).

## Redis storage boundary

Redis owns a standalone go-redis client with automatic retries disabled. One
Redis string holds a default format-1 envelope with schema, unique revision, next generated
integer key and canonical records. A GET provides one consistent collection read.
The compare-and-swap script compares opaque old bytes and executes one final SET;
Lua does not parse numeric keys or make intermediate writes. Only explicit conflict
replies retry; preparation/embedding runs once outside retries. Network/timeout
errors can represent a committed batch and are returned without replay.

Queries and dense search materialize a memory collection, clearing runtime
embedders before inserting stored records. Get/GetMany decode selected records;
all still transfer the entire envelope. The configurable snapshot limit defaults
to 16 MiB. Redis SCAN listing is deduplicated/sorted but not a namespace snapshot.
Metadata is local; hybrid/migrations are unadvertised, and server
persistence/eviction policy determines durability. See [Redis limits](redis.md).

`StorageHash` creates new collections with a format-2 manifest and immutable HASH
records. Existing collections retain their format until `MigrateToHashStorage` is
explicitly called. A uniquely leased generation stages canonical payloads plus
scalar fields and packed vectors with TTLs, then the CAS script checks completion,
persists staged keys and performs the final manifest SET. Only that SET publishes
logical changes. An interrupted script may leave auxiliary keys but cannot publish
a partial batch. Reads verify manifest bytes and fetch the entire generation in
one side-effect-free script; changed manifests retry and missing rows fail closed.
Retirement and `PruneHashStorage` protect current generations and active staging.
Byte budgets include hash projection overhead. `NativeIndexes` builds one Redis
Search index per generation before publication, then verifies manifest/lease,
physical schema and readiness with FT.SEARCH in one Lua execution. Literal TAG
projections preserve NULL/equality semantics; high/low integer NUMERIC projections
preserve all 64 bits. Matching payloads or HNSW candidates are decoded after the
connection is released, with exact Go sorting/scoring and unsupported-case fallback.
FLAT retrieves all matching vector-bearing payloads for exact Go selection,
including roundoff-sensitive distances and ties; HNSW retains approximate recall.
Full-text string hints add TEXT aliases over raw projections in index metadata
version 2. Version 1 retains its original physical schema, including previously
ignored hints; explicit rebuild/native writes add TEXT. Validate observable TEXT
options along with scalar/vector fields. Keyword-query/hybrid APIs remain absent.
Writes rebuild the full index. Index cleanup separately protects current revisions
and active leases; unknown publication results never drop a possibly current index.

`StorageJSON` stages real RedisJSON wrapper documents in format 3. Canonical
payloads and key tokens are strings, preserving all numeric/null semantics without
RedisJSON/Lua numeric conversion. A JSON.TYPE availability probe precedes staging,
including empty creates. JSON.SET/TTL staging and JSON.GET validation precede one
final manifest SET; only explicit conflicts retry. Reads verify manifest/lease and
all wrapper identities in one script. `MigrateToJSONStorage` converts snapshots or
hashes explicitly; acknowledged conversion retires prior hashes/native indexes.
Shared pruning validates known manifest formats and protects the current layout's
generation and active staging. Plain JSON queries/searches retain the Go engine.

RedisJSON native mode uses the same prepublication index readiness and atomic
query guard, with format-3/index-version-4-or-5 metadata and its own index prefix.
Canonical payload strings remain opaque; bounded FLOAT32 vectors project to
internal JSON paths. Literal scalar TAGs and exact 32-bit integer components
support native filtered Query, Count and HNSW/FLAT. Query/scoring run in Go after
fetching matching wrappers in the atomic guard; Count fetches no payloads. Unsupported
filters retain Go evaluation. Version-3 vector-only indexes remain readable without
implicit rebuild; an explicit rebuild/native write adds scalar fields. Full-text
hints select version 5 with a separate raw-string JSON map and safe TEXT aliases.
Version-4 scalar/vector fields stay unchanged until explicit rebuild/native writes;
TEXT uses the same tokenizer/observable schema checks as HASH. Canonical payloads
stay opaque. Keyword/hybrid APIs remain absent. Cleanup protects JSON leases and
current generations, including uncertain writes. See [RedisJSON storage](redis.md#redisjson-storage).
