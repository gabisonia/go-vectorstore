# How It Works

This page explains the internal design of `go-vectorstore` and how requests move through the library.

## 1) High-level architecture

The project is split into two layers:

- `vectordata`: backend-agnostic contracts and primitives.
- `stores/postgres`: PostgreSQL + pgvector implementation.

This keeps the public API stable while allowing additional storage engines later.

## 2) Core data model

The core record type is `vectordata.Record`:

- `ID` (string): primary key.
- `Vector` (`[]float32`): embedding values.
- `Metadata` (`map[string]any`): structured JSON metadata.
- `Content` (`*string`): optional text payload.

Search returns `vectordata.SearchResult`:

- `Record`: matched item.
- `Distance`: raw pgvector distance.
- `Score`: normalized score derived from distance.

## 3) Public API shape

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

## 4) Request flow

### Ensure collection

`PostgresVectorStore.EnsureCollection`:

1. Validates collection spec (`name`, `dimension`, `metric`, `mode`).
2. Ensures `vector` extension (if enabled) and schema.
3. Checks whether the table exists.
4. Creates the table or validates existing schema.
5. Returns a `PostgresCollection` handle.

Default table shape:

```sql
id text primary key,
vector vector(n) not null,
metadata jsonb not null default '{}'::jsonb,
content text
```

### Insert / Upsert

Bulk writes are chunked (`maxRowsPerStatement`) and executed as parameterized SQL.

- `Insert`: plain `INSERT`.
- `Upsert`: `INSERT ... ON CONFLICT (id) DO UPDATE`.

Each record is validated before sending:

- non-empty `ID`
- vector dimension match
- metadata JSON serialization

### Search

`SearchByVector` builds a query plan, then executes it:

1. Validate query vector dimension.
2. Choose operator by metric:
   - cosine: `<=>`
   - l2: `<->`
   - inner product: `<#>`
3. Build `distance` expression:
   - `"vector" <op> $1::vector`
4. Apply optional filter SQL.
5. Apply optional distance threshold.
6. Order by `distance ASC`, limit by `topK`.
7. Scan rows into `SearchResult`.

Score normalization:

- cosine: `1 - distance`
- l2: `1 / (1 + distance)`
- inner product: `-distance`

## 5) Filter system (AST -> SQL)

Filters are represented as an AST in `vectordata`:

- `Eq`, `In`, `Gt`, `Lt`, `Exists`
- `And`, `Or`, `Not`

Fields can target:

- fixed columns (`Column("id")`, `Column("content")`)
- metadata JSON paths (`Metadata("category")`, `Metadata("a", "b")`)

`CompileFilterSQL` converts AST to:

- SQL predicate fragment
- parameter list

This keeps SQL injection-safe behavior by binding values as query args.

## 6) Schema safety modes

`CollectionSpec.Mode` controls ensure behavior:

- `EnsureStrict`:
  - fail if expected columns/type/dimension mismatch.
- `EnsureAutoMigrate`:
  - can add missing optional columns (`metadata`, `content`).

Dimension is always validated against `vector(n)` and must match.

## 7) Index management

`EnsureIndexes` supports:

- Vector index:
  - HNSW (default for vector index creation)
  - IVFFlat (optional)
- Metadata index:
  - GIN on `metadata` JSONB
  - optional `jsonb_path_ops`

Metric-specific operator classes are selected automatically:

- cosine -> `vector_cosine_ops`
- l2 -> `vector_l2_ops`
- inner product -> `vector_ip_ops`

## 8) Typed extension

MVP public API is record-based.  
An optional typed wrapper exists:

- `vectordata.Codec[T]`
- `vectordata.TypedCollection[T]`

This lets application code work with domain models while storage stays record-oriented.

## 9) Error model

Common exported errors:

- `ErrNotFound`
- `ErrDimensionMismatch`
- `ErrSchemaMismatch`
- `ErrInvalidFilter`

Errors are wrapped with context so callers can use `errors.Is(...)` against base error types.

## 10) Semantic-search sample flow

The sample app in `samples/semantic-search` does:

1. Build embeddings for 3 fake articles via OpenAI embeddings API.
2. Ensure a Postgres vector collection.
3. Upsert article records.
4. Ensure vector + metadata indexes.
5. Embed user query and run semantic search.
6. Print ranked top matches with score and distance.

## 11) Current scope and limits

Current MVP scope:

- Postgres + pgvector only.
- single-vector column per collection.
- metadata filtering through a focused AST.

Future extension points:

- additional backends under `stores/`.
- richer filter operators.
- optional reranking strategies.
