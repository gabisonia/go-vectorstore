# Stores Implementation Design

This document explains how `go-vectorstore` store backends are implemented, how they are structured, and what behavior is important when you use or extend them.

## 1) Design Goals

- Keep a single backend-agnostic public API in `vectordata`
- Isolate backend specifics inside `stores/<backend>`
- Enforce schema and vector-dimension correctness at runtime
- Keep write/read/search behavior consistent across implementations
- Allow adding future store implementations without changing caller-facing contracts

## 2) Layering and Boundaries

The implementation is split into:

- `vectordata`: shared contracts, record/search types, filter AST, filter SQL compiler, typed codec wrapper, error model
- `stores/postgres`: PostgreSQL + pgvector implementation

Each backend implements:

- a `VectorStore` implementation (`EnsureCollection`, `Collection`)
- a `Collection` implementation (`Insert`, `Upsert`, `Get`, `Delete`, `Count`, `SearchByVector`, `EnsureIndexes`)

## 3) Shared Core Contracts (`vectordata`)

Main types:

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

1. Validate `topK` and query vector dimension
2. Resolve metric operator (`<=>`, `<->`, `<#>`)
3. Build distance expression and dynamic projection columns
4. Compile optional filter AST into SQL + bind args
5. Apply optional threshold (`distance <= threshold`)
6. Order by ascending distance and limit by `topK`
7. Scan rows and map distance to score

### 4.5 Index Management

`EnsureIndexes` can create:

- Vector index (HNSW or IVFFlat)
  - metric-specific opclass: `vector_cosine_ops`, `vector_l2_ops`, `vector_ip_ops`
  - defaults:
    - HNSW: `m=16`, `ef_construction=64`
    - IVFFlat: `lists=100`
- Metadata GIN index
  - optional `jsonb_path_ops`

## 5) Filter System and Execution Model

Filter AST supports:

- `Eq`, `In`, `Gt`, `Lt`, `Exists`
- `And`, `Or`, `Not`
- Fields: fixed columns (`id`, `content`) and metadata JSON paths

Execution strategy by backend:

- Postgres: compile AST -> parameterized SQL via `CompileFilterSQL`

Important behavior:

- Invalid AST structures return `ErrInvalidFilter`
- Numeric comparisons are numeric when values are numeric; otherwise textual comparison is used
- Missing fields typically evaluate as non-match, except `Exists` which reports presence

## 6) Schema Safety Modes

`CollectionSpec.Mode` controls schema handling:

- `EnsureStrict`: fail on mismatch/missing required expectations
- `EnsureAutoMigrate`: add missing optional columns (`metadata`, `content`) where supported

Mode default comes from backend options:

- `StrictByDefault=true` -> strict mode when spec mode is unset
- `StrictByDefault=false` -> auto-migrate mode when spec mode is unset

## 7) Invariants

These rules are enforced in all current implementations:

- Collection name must be non-empty
- Dimension must be greater than zero
- Metric must be supported (`cosine`, `l2`, `inner_product`)
- Record ID must be non-empty for writes
- Query and record vectors must match collection dimension
- Nil metadata is normalized to empty object
- `Get` returns `ErrNotFound` on missing ID

## 8) Extending with a New Backend

To add a new store backend, follow the same contract shape:

1. Implement `VectorStore` and `Collection`
2. Add robust `EnsureCollection` validation and mode behavior
3. Enforce vector dimension checks on write/search
4. Support filter handling (compiled or in-process)
5. Reuse `vectordata` errors and scoring semantics
6. Add integration tests matching existing backend test coverage style

For a practical checklist and repository wiring steps, see [`connector-development.md`](connector-development.md).
