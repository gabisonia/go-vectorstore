# go-vectorstore

[![Go Reference](https://pkg.go.dev/badge/github.com/gabisonia/go-vectorstore.svg)](https://pkg.go.dev/github.com/gabisonia/go-vectorstore)
[![Latest Release](https://img.shields.io/github/v/release/gabisonia/go-vectorstore?sort=semver)](https://github.com/gabisonia/go-vectorstore/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gabisonia/go-vectorstore)](https://github.com/gabisonia/go-vectorstore/blob/master/go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A lightweight Go vector-store library inspired by `Microsoft.Extensions.VectorData`.

Scope:
- Go 1.25+
- PostgreSQL + `pgvector`, embedded SQLite, standalone Redis snapshots and in-memory model storage
- Model-driven collections with typed/native keys, named vectors, struct tags, and optional embeddings
- Shared model contracts and conformance tests across providers
- Original Record API and custom codecs remain supported

This library can be used to build retrieval systems such as:
- semantic search
- RAG pipelines (retrieve from vector DB, then generate with an LLM)
- context-aware assistants grounded on your own data

## Requirements

The minimum Go version is now 1.25 because the patched pgx and text dependencies
require it. Use current stable Go for up-to-date standard-library security fixes.

- Go 1.25+
- PostgreSQL provider: PostgreSQL 16+ with `pgvector` extension
- SQLite provider: a writable database file; no database server or C toolchain required
- Redis provider: standalone Redis; whole-collection snapshots and Go exact search ([limits](docs/redis.md))
- Docker (optional, for integration tests and sample compose flows)

## Project layout

- `vectordata`: backend-agnostic core interfaces, record model, filters, typed wrapper
- `stores/postgres`: Postgres implementation with `pgxpool`
- `stores/sqlite`: durable SQLite model collections with exact search
- `stores/redis`: atomic Redis model snapshots with dense exact search
- `stores/memory`: transient model collections for development and contract tests
- `samples`: runnable demos (see `samples/README.md`)
- `docs`: architecture and implementation notes

## Install

```bash
go get github.com/gabisonia/go-vectorstore@latest
```

Use the [release list](https://github.com/gabisonia/go-vectorstore/releases) to
check available tags and pin the resolved version in go.mod. This checkout
prepares v0.4.1; its tag becomes available after release publication. See the
[upgrade guide](docs/upgrading.md) and [compatibility policy](docs/releases.md).

## Quick Start (Local)

For typed text retrieval with no server or API key:

```sh
go run ./samples/typed-search -category architecture
go run ./samples/typed-rag
```

These commands are on master and included in source versions from v0.4.1. Fixture
mode uses an instructional deterministic embedder. See the [sample index](samples/README.md)
for real OpenAI opt-in, provider selection, expected output, and cleanup.
For coding-agent integrations, start with the [AI usage guide](docs/ai-guide.md)
and [documentation discovery index](llms.txt).

Run the persistent SQLite example without a server or API key:

```sh
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db
```

For the PostgreSQL semantic-search example:

1. Start local Postgres + `pgvector` from the repository root:

```bash
docker compose up -d postgres
```

2. Export your OpenAI key and run the semantic sample:

```bash
export OPENAI_API_KEY=your_key_here
go run ./samples/semantic-search -q "how can I reduce cloud costs?"
```

3. Stop local services when done:

```bash
docker compose down
```

## Model-driven API

The model API follows the key/data/vector-property design of
`Microsoft.Extensions.VectorData`. Define Go structs with `vectordata` tags or
provide an explicit `CollectionDefinition`, then use
`EnsureTypedModelCollection[K,T]` for typed CRUD, filtered retrieval and search.
PostgreSQL stores model fields in native columns and creates the declared indexes.

```go
type Hotel struct {
    ID          int64     `vectordata:"key,generated,name=hotel_id"`
    Name        string    `vectordata:"data,index"`
    Description string    `vectordata:"data,fulltext"`
    Tags        []string  `vectordata:"data,index"`
    Embedding   []float32 `vectordata:"vector,dimensions=3,index,source=Description"`
}
```

Run a complete example without an API key:

```sh
go run ./samples/model-store
```

The same sample accepts `-sqlite /path/to/models.db` for embedded storage,
`-dsn` to use PostgreSQL and `-embedding-batch-size` to
bound embedding requests. It demonstrates store-wide embedding defaults
with a deterministic instructional embedder, typed reads,
array filtering, and generated keys. Use a real embedding model for semantic search.

See [the model API guide](docs/model-api.md) for collection lifecycle, explicit
schemas, native UUID/int64/uint64 keys, generated UUIDs, UUID/byte data, date-only/time-only properties,
exact decimals, and native numeric/bool/UUID/timestamp/calendar arrays,
multiple vectors, iterator APIs, embeddings and
PostgreSQL hybrid search. Models can opt into float16 (`halfvec`) storage with
`precision=float16` while keeping `[]float32` inputs and outputs. Stores also expose optional `CapabilityProvider`
metadata for supported keys, data types, metrics, index combinations and hybrid search.
Optional [store and collection metadata](docs/model-api.md#store-and-collection-metadata)
reports system, database, namespace and collection identity without database I/O.
All providers support dense Manhattan (`DistanceL1`) search; PostgreSQL supports L1 HNSW
indexes and rejects unsupported L1/IVFFlat combinations before database work.
Models can also use packed `BinaryVector` fields with `kind=binary`, Hamming
distance and `SearchBinary`. PostgreSQL provides native `bit(n)` storage,
HNSW/IVFFlat indexes and binary hybrid search.
Sparse models use `SparseVector` with zero-based coordinates, `kind=sparse`, and
`SearchSparse`. Memory, SQLite and PostgreSQL support cosine/L2/squared-L2/inner-product/L1 sparse search;
PostgreSQL adds `sparsevec(n)` storage, HNSW indexes and sparse hybrid search.
PostgreSQL offers [model migration planning and explicit additive application](docs/migrations.md)
with schema drift detection, reviewable SQL and backfill/re-embedding classifications.
Vectors may be nullable with `optional` (or pointer fields). Missing vectors are
stored without an embedding and excluded from searches on that property.
`ContainsAny` and `ContainsAll` support array overlap and containment; PostgreSQL
uses native predicates for indexed native arrays.

See the [provider and .NET parity checklist](docs/parity.md), [SQLite guide](docs/sqlite.md),
[upgrade guide](docs/upgrading.md), and [changelog](CHANGELOG.md).
Original `Record`/`Collection` callers remain compatible.
Bulk PostgreSQL writes automatically use binary vector parameters to reduce
serialization allocations; see [the measured transport comparison](docs/performance.md#binary-vector-ingestion-and-copy-comparison).


## Record API Walkthrough

```go
package main

import (
    "context"
    "fmt"

    "github.com/gabisonia/go-vectorstore/stores/postgres"
    "github.com/gabisonia/go-vectorstore/vectordata"
    "github.com/jackc/pgx/v5/pgxpool"
)

func main() {
    ctx := context.Background()
    pool, err := pgxpool.New(ctx, "postgres://postgres:postgres@localhost:54329/vectorstore_test?sslmode=disable")
    if err != nil {
        panic(err)
    }
    defer pool.Close()

    store, err := postgres.NewVectorStore(pool, postgres.DefaultStoreOptions())
    if err != nil {
        panic(err)
    }

    collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{
        Name:      "docs",
        Dimension: 3,
        Metric:    vectordata.DistanceCosine,
        Mode:      vectordata.EnsureStrict,
    })
    if err != nil {
        panic(err)
    }

    records := []vectordata.Record{
        {
            ID:       "doc-1",
            Vector:   []float32{0.1, 0.2, 0.3},
            Metadata: map[string]any{"category": "news", "rank": 10},
        },
        {
            ID:       "doc-2",
            Vector:   []float32{0.2, 0.1, 0.2},
            Metadata: map[string]any{"category": "blog", "rank": 5},
        },
    }
    if err := collection.Upsert(ctx, records); err != nil {
        panic(err)
    }

    if err := collection.EnsureIndexes(ctx, vectordata.IndexOptions{
        Vector: &vectordata.VectorIndexOptions{
            Method: vectordata.IndexMethodHNSW,
            Metric: vectordata.DistanceCosine,
            HNSW: vectordata.HNSWOptions{
                M:              16,
                EfConstruction: 64,
            },
        },
        Metadata: &vectordata.MetadataIndexOptions{UsePathOps: true},
    }); err != nil {
        panic(err)
    }

    filter := vectordata.And(
        vectordata.Eq(vectordata.Metadata("category"), "news"),
        vectordata.Gt(vectordata.Metadata("rank"), 6),
    )

    results, err := collection.SearchByVector(
        ctx,
        []float32{0.1, 0.2, 0.25},
        5,
        vectordata.SearchOptions{Filter: filter},
    )
    if err != nil {
        panic(err)
    }

    for _, r := range results {
        fmt.Printf("id=%s score=%.4f distance=%.4f\n", r.Record.ID, r.Score, r.Distance)
    }
}
```

All vector elements must be finite. Cosine collections reject zero vectors for
both writes and queries with `ErrInvalidVector`; L2 and inner-product collections
allow zero vectors. Write validation errors also match `ErrInvalidRecord`.

Metadata path segments are literal JSON keys, including empty strings and
whitespace. `Metadata(" price ")` targets the key `" price "`; callers that relied
on automatic trimming must pass the intended key explicitly. Fixed column names
continue to be trimmed.

## Search Options

`SearchByVector` supports filtering, distance thresholding, and projection control.

```go
threshold := 0.35
projection := &vectordata.Projection{
    IncludeMetadata: true,
    IncludeContent:  true,
    IncludeVector:   false,
}

results, err := collection.SearchByVector(ctx, queryVector, 10, vectordata.SearchOptions{
    Filter:     vectordata.Eq(vectordata.Metadata("category"), "backend"),
    Threshold:  &threshold,
    Projection: projection,
})
```

If `Projection` is `nil`, the default projection includes `Metadata` and `Content`, but not `Vector`.
`Threshold` is compared to the backend distance value, so lower is stricter. It is not a normalized score cutoff.

The Postgres backend returns numeric metadata as `json.Number` (including nested
values) to preserve precision. Code that previously asserted `float64` should use
`json.Number` instead, for example:

```go
rank, err := result.Record.Metadata["rank"].(json.Number).Int64()
```

Import `encoding/json` for `json.Number`. Numeric metadata filters accept Go
numeric values and `json.Number`; numeric-looking strings outside PostgreSQL's
`numeric` range are excluded from numeric comparisons.

Approximate-search tuning can be set per request:

```go
results, err := collection.SearchByVector(ctx, queryVector, 10, vectordata.SearchOptions{
    Filter: vectordata.Eq(vectordata.Metadata("category"), "backend"),
    HNSW: &vectordata.HNSWSearchOptions{
        EfSearch:      100,
        IterativeScan: true,
        MaxScanTuples: 20000,
    },
})
```

IVFFlat uses `IVFFlat: &vectordata.IVFFlatSearchOptions{Probes: 10,
IterativeScan: true, MaxProbes: 100}`. Zero numeric values retain server defaults.
Iterative scans and their work limits require pgvector 0.8+. Settings apply within
the search transaction and do not leak to other pooled requests. Results remain
sorted by distance; approximate indexes and bounded scans can still return fewer
than `topK` matches.

Metadata `Eq` and `In` filters can use the GIN metadata index. Numeric range
comparisons, `Exists`, array-index paths, and negated filters need other indexing
strategies. Existing indexes are checked for compatibility; a conflicting
definition returns `ErrSchemaMismatch`.

## Store Options

```go
store, err := postgres.NewVectorStore(pool, postgres.StoreOptions{
    Schema:          "public",
    EnsureExtension: true,
    StrictByDefault: true,
})
```

- `Schema`: SQL schema for collection tables
- `EnsureExtension`: auto-runs `CREATE EXTENSION IF NOT EXISTS vector`
- `StrictByDefault`: default ensure mode when `CollectionSpec.Mode` is not set

## Integration tests

```bash
go test -tags=integration ./...
```

Unit tests run with:

```bash
go test ./...
```

Notes:
- Integration tests start Postgres/pgvector automatically via Testcontainers
- Docker daemon must be available when running integration tests
- Optional override: set `PGVECTOR_TEST_DSN` to use an existing Postgres instance instead of starting a container
- Set `PGVECTOR_TEST_IMAGE=pgvector/pgvector:pg18` to test a different PostgreSQL image (the default is `pgvector/pgvector:pg16`)

## Docker Compose (optional)

`docker-compose.yml` at the repository root is kept for manual local runs.

- Use root `docker-compose.yml` when you want a persistent local Postgres+pgvector instance outside tests
- Use Testcontainers (`go test -tags=integration ./...`) for integration tests
- Sample apps have their own compose files at `samples/semantic-search/docker-compose.yml` and `samples/ragrimosa/docker-compose.yml`
- Sample Dockerfiles build with `golang:1.27-alpine`; the library minimum is Go 1.25

## Release Automation

GitHub Actions workflows are configured for:

- CI on pushes/PRs with race detection, `go vet`, release-script tests, and vulnerability scanning, testing Go 1.25/current stable and PostgreSQL 16/18 (`.github/workflows/ci.yml`)
- release publishing from semver tags or manual workflow dispatch (`.github/workflows/release.yml`), gated on the same CI checks

Release version input is passed through environment variables and validated before
any publishing job runs. Existing tags must point to the checked-out, tested
commit; the workflow will not move a tag to another commit. Prerelease versions
are published as GitHub prereleases.

`VERSION` selects the source version being prepared or tagged (`0.4.1`). It does
not establish that the tag has been published. Choose an unused version and update
`VERSION` and the changelog in a reviewed PR before publishing from master. Published tags are immutable; documentation updates after
a release do not change the source stored under that tag.

Release options for that new version:

1. Manual (recommended): run `Release` workflow on master via GitHub UI with the new `version` input, with or without leading `v`.
2. Tag-driven: push a semver tag and the workflow publishes release notes automatically:

```bash
RELEASE_VERSION="$(cat VERSION)" # After updating VERSION to the unused release version.
git tag "v${RELEASE_VERSION}"
git push origin "v${RELEASE_VERSION}"
```

## Samples

- Overview and entry points: [`samples/README.md`](samples/README.md)
- Local agent with Ollama and SQLite: [`local-doc-agent`](samples/local-doc-agent/README.md) (Docker Compose, separate module)
- Redis model snapshots and exact retrieval: [`redis-store`](samples/redis-store/README.md)
- Typed semantic search and RAG: [`typed-search`](samples/typed-search/README.md), [`typed-rag`](samples/typed-rag/README.md)
- Focused model workflows: [`recipes`](samples/recipes/README.md)
- Model-driven collections (no API key): [`samples/model-store`](samples/model-store/README.md)
- Persistent SQLite and optional vectors (no API key): [`samples/sqlite-store`](samples/sqlite-store/README.md)
- Semantic search sample app: `samples/semantic-search`
- RAG sample app (`RAGrimosa`): `samples/ragrimosa`

## Documentation

- Production operation: [`docs/production.md`](docs/production.md)
- Compatibility and release policy: [`docs/releases.md`](docs/releases.md)
- Private vulnerability reporting and support: [`SECURITY.md`](SECURITY.md)

- AI application usage: [`docs/ai-guide.md`](docs/ai-guide.md)
- Repository agent instructions: [`AGENTS.md`](AGENTS.md)
- Documentation discovery: [`llms.txt`](llms.txt)

- Docs index: [`docs/README.md`](docs/README.md)
- Internals and architecture: [`docs/architecture.md`](docs/architecture.md)
- Connector development guide: [`docs/connector-development.md`](docs/connector-development.md)
- Model-driven API: [`docs/model-api.md`](docs/model-api.md)
- SQLite provider: [`docs/sqlite.md`](docs/sqlite.md)
- Redis provider: [`docs/redis.md`](docs/redis.md)

- Provider and .NET parity: [`docs/parity.md`](docs/parity.md)
- Upgrade guide: [`docs/upgrading.md`](docs/upgrading.md)
- Model migrations: [`docs/migrations.md`](docs/migrations.md)
- Changelog: [`CHANGELOG.md`](CHANGELOG.md)
- Benchmarks and performance notes: [`docs/performance.md`](docs/performance.md)

Redis defaults to compatible snapshot storage. Opt-in hash generations and an
explicit storage upgrade expose indexable fields while keeping atomic batches;
native query/index execution is a later increment.

## License

This project is licensed under the MIT License.
