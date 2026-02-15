# Stores Implementation Design

This document explains how `go-vectorstore` store backends are implemented, how they are structured, and what behavior is important when you use or extend them.

## 1) Design Goals

- Keep a single backend-agnostic public API in `vectordata`
- Isolate backend specifics inside `stores/<backend>`
- Enforce schema and vector-dimension correctness at runtime
- Keep write/read/search behavior consistent across backends where possible
- Allow adding future store implementations without changing caller-facing contracts

## 2) Layering and Boundaries

The implementation is split into:

- `vectordata`: shared contracts, record/search types, filter AST, filter SQL compiler, typed codec wrapper, error model
- `stores/postgres`: PostgreSQL + pgvector implementation
- `stores/mssql`: SQL Server implementation

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

## 5) MSSQL Store (`stores/mssql`)

### 5.1 Main Components

- `MSSQLVectorStore` (`store.go`)
  - Owns `*sql.DB` and options (`Schema`, `StrictByDefault`)
  - Validates/normalizes collection specs
  - Ensures schema and metadata table
  - Creates/validates collection table
- `MSSQLCollection` (`collection.go`)
  - Implements collection operations
  - Uses transactional batched writes
  - Executes similarity + filters in process for this MVP
- Schema utilities (`schema.go`)
  - Ensures schema
  - Ensures internal metadata table `__vector_collections`
  - Validates collection table columns/PK
  - Stores/validates logical dimension and metric in metadata table
- Filter evaluator (`filter_eval.go`)
  - Evaluates filter AST directly against loaded records (column + metadata paths)
- Helpers (`helpers.go`)
  - JSON encoding/decoding for vector and metadata
  - Distance math (cosine/l2/inner product)
  - projection resolver and type helpers

### 5.2 Data Model and Metadata Strategy

MSSQL MVP stores:

- vector as `NVARCHAR(MAX)` JSON (not native vector type)
- metadata as `NVARCHAR(MAX)` JSON
- content as `NVARCHAR(MAX)` nullable

Because vector dimension and metric are not part of physical type, they are tracked in `__vector_collections`:

- `name` (PK)
- `dimension`
- `metric`

`EnsureCollection` validates both table shape and metadata row consistency.

### 5.3 Write / Read / Search Path

Write behavior:

- Validate `ID` and vector dimension
- Serialize vector + metadata to JSON
- Chunk in batches of 500
- Use transaction per chunk
- Upsert strategy:
  - execute single-statement `UPDATE ... WITH (UPDLOCK, SERIALIZABLE)` + conditional `INSERT`
  - avoids race conditions under concurrent upserts for the same ID

Search behavior (MVP):

1. Validate `topK` and vector dimension
2. Stream records row-by-row from SQL
3. Evaluate filter AST in-process (`matchesFilter`)
4. Compute distance in-process (`distanceBetween`)
5. Apply optional threshold
6. Maintain bounded top-k max-heap in memory (`O(k)` memory)
7. Return results sorted by distance asc (tie-break by `ID`)

`EnsureIndexes` is intentionally a no-op in MSSQL backend for this MVP.

## 6) Filter System and Execution Model

Filter AST supports:

- `Eq`, `In`, `Gt`, `Lt`, `Exists`
- `And`, `Or`, `Not`
- Fields: fixed columns (`id`, `content`) and metadata JSON paths

Execution strategy by backend:

- Postgres: compile AST -> parameterized SQL via `CompileFilterSQL`
- MSSQL: evaluate AST in Go against streamed records

Important behavior:

- Invalid AST structures return `ErrInvalidFilter`
- Numeric comparisons are numeric when values are numeric; otherwise textual comparison is used
- Missing fields typically evaluate as non-match, except `Exists` which reports presence

## 7) Schema Safety Modes

`CollectionSpec.Mode` controls schema handling:

- `EnsureStrict`: fail on mismatch/missing required expectations
- `EnsureAutoMigrate`: add missing optional columns (`metadata`, `content`) where supported

Mode default comes from backend options:

- `StrictByDefault=true` -> strict mode when spec mode is unset
- `StrictByDefault=false` -> auto-migrate mode when spec mode is unset

## 8) Cross-Backend Invariants

These rules are enforced in both backends:

- Collection name must be non-empty
- Dimension must be greater than zero
- Metric must be supported (`cosine`, `l2`, `inner_product`)
- Record ID must be non-empty for writes
- Query and record vectors must match collection dimension
- Nil metadata is normalized to empty object
- `Get` returns `ErrNotFound` on missing ID

## 9) Key Differences: Postgres vs MSSQL

- Vector storage:
  - Postgres: native `vector(n)` type via pgvector
  - MSSQL: JSON string payload (`NVARCHAR(MAX)`)
- Search execution:
  - Postgres: SQL-level distance computation and ordering
  - MSSQL: in-memory computation after loading rows
- Filter execution:
  - Postgres: SQL compilation and pushdown
  - MSSQL: in-process AST evaluation
- Indexing:
  - Postgres: vector + metadata index creation supported
  - MSSQL: no-op in MVP
- Metric/dimension persistence:
  - Postgres: inferred/validated from physical `vector(n)` column
  - MSSQL: persisted in `__vector_collections`

## 10) Extending with a New Backend

To add a new store backend, follow the same contract shape:

1. Implement `VectorStore` and `Collection`
2. Add robust `EnsureCollection` validation and mode behavior
3. Enforce vector dimension checks on write/search
4. Support filter handling (compiled or in-process)
5. Reuse `vectordata` errors and scoring semantics
6. Add integration tests matching existing backend test coverage style
