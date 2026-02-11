# How It Works

This page explains the internal design of `go-vectorstore` and how requests move through the library.

## 1) High-level architecture

The project is split into two layers:

- `vectordata`: backend-agnostic contracts and primitives.
- `stores/postgres`: [PostgreSQL](https://www.postgresql.org/docs/current/index.html) + [pgvector](https://github.com/pgvector/pgvector) implementation.

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

The pgvector operators are documented in the [pgvector README](https://github.com/pgvector/pgvector#querying).

Score normalization:

- cosine: `1 - distance` ([cosine similarity](https://en.wikipedia.org/wiki/Cosine_similarity))
- l2: `1 / (1 + distance)` ([Euclidean distance / L2](https://en.wikipedia.org/wiki/Euclidean_distance))
- inner product: `-distance` ([dot product / inner product](https://en.wikipedia.org/wiki/Dot_product))

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
JSON path extraction behavior comes from PostgreSQL [JSON/JSONB functions and operators](https://www.postgresql.org/docs/current/functions-json.html).

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

For vector indexing details:

- HNSW: [original paper](https://arxiv.org/abs/1603.09320) and [overview](https://en.wikipedia.org/wiki/Hierarchical_navigable_small_world).
- IVFFlat: [FAISS index reference](https://github.com/facebookresearch/faiss/wiki/Faiss-indexes) and [pgvector IVFFlat docs](https://github.com/pgvector/pgvector#ivfflat).

For metadata indexing details:

- GIN index basics: [PostgreSQL GIN indexes](https://www.postgresql.org/docs/current/gin.html).
- JSONB operators / indexing context: [PostgreSQL JSON functions and operators](https://www.postgresql.org/docs/current/functions-json.html).

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

## 12) References

- pgvector project docs: https://github.com/pgvector/pgvector
- PostgreSQL docs: https://www.postgresql.org/docs/current/index.html
- ANN concept: https://en.wikipedia.org/wiki/Nearest_neighbor_search
- HNSW paper: https://arxiv.org/abs/1603.09320
- FAISS index families (including IVF): https://github.com/facebookresearch/faiss/wiki/Faiss-indexes
