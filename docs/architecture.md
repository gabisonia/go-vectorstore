# Architecture and Request Flow

This page explains the internal design of `go-vectorstore` and how requests move through the library.

## 1) Layering

The project is split into two layers:

- `vectordata`: backend-agnostic contracts and primitives
- `stores/postgres`: PostgreSQL + pgvector implementation
- `stores/mssql`: SQL Server implementation

This keeps the public API stable while allowing additional storage engines later.

## 2) Core Data Model

The base record type is `vectordata.Record`:

- `ID` (`string`): primary key
- `Vector` (`[]float32`): embedding values
- `Metadata` (`map[string]any`): structured JSON metadata
- `Content` (`*string`): optional text payload

Search returns `vectordata.SearchResult`:

- `Record`: matched item
- `Distance`: backend-computed distance value
- `Score`: normalized score derived from distance

Score normalization:

- cosine: `1 - distance`
- l2: `1 / (1 + distance)`
- inner product: `-distance`

## 3) Public API Shape

Main interfaces:

- `vectordata.VectorStore`
- `vectordata.Collection`

Main operations:

- `EnsureCollection`
- `Insert`, `Upsert`
- `Get`, `Delete`, `Count`
- `SearchByVector`
- `EnsureIndexes`

All methods require `context.Context`.

## 4) Runtime Defaults and Configuration

`postgres.StoreOptions` defaults (`postgres.DefaultStoreOptions()`):

- `Schema`: `public`
- `EnsureExtension`: `true`
- `StrictByDefault`: `true`

`mssql.StoreOptions` defaults (`mssql.DefaultStoreOptions()`):

- `Schema`: `dbo`
- `StrictByDefault`: `true`

Collection defaults:

- Metric defaults to cosine when omitted
- Ensure mode defaults to:
  - `EnsureStrict` if `StrictByDefault=true`
  - `EnsureAutoMigrate` if `StrictByDefault=false`

Other operational defaults:

- Writes are chunked at `maxRowsPerStatement=500`
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
- metadata JSON serialization

### Postgres: Search

`SearchByVector` builds a query plan, then executes it:

1. Validates `topK > 0`
2. Validates query vector dimension
3. Chooses operator by metric:
   - cosine: `<=>`
   - l2: `<->`
   - inner product: `<#>`
4. Builds `distance` expression (`"vector" <op> $1::vector`)
5. Applies optional filter SQL
6. Applies optional distance threshold (`distance <= threshold`)
7. Orders by `distance ASC`, limits by `topK`
8. Scans rows into `SearchResult`

The pgvector operators are documented in the [pgvector README](https://github.com/pgvector/pgvector#querying).

### MSSQL: Ensure / Search

`MSSQLVectorStore.EnsureCollection`:

1. Ensures target schema exists
2. Ensures internal collection metadata table exists
3. Creates or validates the collection table
4. Persists and validates dimension/metric metadata

`MSSQLCollection.SearchByVector`:

1. Streams records from SQL Server
2. Evaluates filters against records in-process
3. Computes distance in-process (cosine/l2/inner product)
4. Applies threshold and keeps a bounded in-memory top-k heap
5. Returns top-k sorted by distance

## 6) Filter System (AST -> SQL)

Filters are represented as an AST in `vectordata`:

- `Eq`, `In`, `Gt`, `Lt`, `Exists`
- `And`, `Or`, `Not`

Fields can target:

- fixed columns (`Column("id")`, `Column("content")`)
- metadata JSON paths (`Metadata("category")`, `Metadata("a", "b")`)

`CompileFilterSQL` converts AST to:

- SQL predicate fragment
- parameter list

Behavior details:

- SQL injection safety is preserved by binding values as query args
- metadata `Eq`/`In` compares JSONB values (`::jsonb`), so value types matter
- metadata `Gt`/`Lt` uses numeric comparison when the input is numeric, otherwise text comparison
- column filters are whitelist-based (`id`, `content` in the postgres backend)

JSON path extraction behavior comes from PostgreSQL [JSON/JSONB functions and operators](https://www.postgresql.org/docs/current/functions-json.html).

For MSSQL in this MVP, the same AST is evaluated in-process against loaded records.

## 7) Schema Safety Modes

`CollectionSpec.Mode` controls ensure behavior:

- `EnsureStrict`:
  - fails if expected columns/type/dimension mismatch
- `EnsureAutoMigrate`:
  - can add missing optional columns (`metadata`, `content`)

Dimension is always validated against `vector(n)` and must match.

## 8) Index Management

`EnsureIndexes` supports:

- Vector index:
  - HNSW (default method)
  - IVFFlat (optional)
- Metadata index:
  - GIN on `metadata` JSONB
  - optional `jsonb_path_ops`

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

MVP public API is record-based.

An optional typed wrapper exists:

- `vectordata.Codec[T]`
- `vectordata.TypedCollection[T]`

This lets application code work with domain models while storage stays record-oriented.

## 10) Error Model

Common exported errors:

- `ErrNotFound`
- `ErrDimensionMismatch`
- `ErrSchemaMismatch`
- `ErrInvalidFilter`

Errors are wrapped with context so callers can use `errors.Is(...)` against base error types.

## 11) Sample App Flows

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

Current MVP scope:

- Postgres + pgvector backend
- MSSQL backend (vectors stored as JSON payloads)
- single-vector column per collection
- metadata filtering through a focused AST

Future extension points:

- additional backends under `stores/`
- richer filter operators
- optional reranking strategies
- native MSSQL vector indexing/query pushdown

## 13) References

- pgvector project docs: https://github.com/pgvector/pgvector
- PostgreSQL docs: https://www.postgresql.org/docs/current/index.html
- SQL Server docs: https://learn.microsoft.com/sql/sql-server/
