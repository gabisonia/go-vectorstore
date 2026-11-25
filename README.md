# go-vectorstore

[![Go Reference](https://pkg.go.dev/badge/github.com/gabisonia/go-vectorstore.svg)](https://pkg.go.dev/github.com/gabisonia/go-vectorstore)
[![Latest Release](https://img.shields.io/github/v/release/gabisonia/go-vectorstore?sort=semver)](https://github.com/gabisonia/go-vectorstore/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gabisonia/go-vectorstore)](https://github.com/gabisonia/go-vectorstore/blob/master/go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Store typed Go structs and search them by vector. Start with a SQLite file;
the same model API also works with PostgreSQL, Redis, Qdrant and in-memory
collections. [Documentation](https://gabisonia.dev/go-vectorstore/) · [Provider differences](docs/parity.md)

## Your first SQLite search

With Go 1.26 or later, start in an empty directory and run:

```sh
go mod init example.com/handbook
go get github.com/gabisonia/go-vectorstore/stores/sqlite@v1.0.0
```

Save this as `main.go`. Each handbook page has a two-number vector describing
how strongly it relates to **recovery** and **deployment**, in that order.
The values are hand-written so you can see exactly why a query matches.

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/gabisonia/go-vectorstore/stores/sqlite"
    "github.com/gabisonia/go-vectorstore/vectordata"
)

type Page struct {
    ID     string    `vectordata:"key"`
    Title  string    `vectordata:"data"`
    Vector []float32 `vectordata:"vector,dimensions=2,metric=cosine"`
}

func main() {
    if err := search(); err != nil {
        log.Fatal(err)
    }
}

func search() error {
    ctx := context.Background()
    store, err := sqlite.Open(ctx, "handbook.db", sqlite.StoreOptions{})
    if err != nil {
        return err
    }
    defer store.Close()

    pages, err := vectordata.EnsureTypedModelCollection[string, Page](
        ctx, store, "pages", nil, vectordata.ModelCollectionOptions{})
    if err != nil {
        return err
    }
    _, err = pages.Upsert(ctx, []Page{
        {"restore", "Restore a database backup", []float32{1, 0}},
        {"rollback", "Roll back a deployment", []float32{0.7, 0.7}},
        {"deploy", "Deploy a new service", []float32{0, 1}},
    })
    if err != nil {
        return err
    }
    hits, err := pages.Search(ctx, []float32{1, 0}, 1, vectordata.ModelSearchOptions{})
    if err != nil {
        return err
    }
    for _, hit := range hits {
        fmt.Println(hit.Item.Title)
    }
    return nil
}
```

Run `go run .`. The recovery query points in the same direction as the first
page's vector, so cosine search prints:

```text
Restore a database backup
```

Try changing the vector passed to `pages.Search` to `[]float32{0, 1}` and run it again to find the
deployment page. SQLite keeps the collection in `handbook.db`; upserting the
same IDs updates the same three pages. Delete the demo directory when finished.

## Requirements

This checkout requires Go 1.26 or later for its patched dependencies. The
published v0.5.0 release supports Go 1.25.

- Go 1.26+
- PostgreSQL provider: PostgreSQL 16+ with `pgvector` extension
- Qdrant provider: standalone Qdrant 1.20.2 with explicit snapshot/receipt limits ([scope](docs/qdrant.md))
- SQLite provider: a writable database file; no database server or C toolchain required
- Redis provider: standalone snapshots with Go exact search; opt-in HNSW/FLAT, scalar and string full-text indexes ([limits](docs/redis.md))
- Docker (optional, for integration tests and Aspire sample services)
- Aspire CLI 17.0.0 and Go 1.26+ for sample orchestration

## Project layout

- `vectordata`: backend-agnostic core interfaces, record model, filters, typed wrapper
- `stores/postgres`: Postgres implementation with `pgxpool`
- `stores/sqlite`: durable SQLite model collections with exact search and opt-in scalar indexes
- `stores/sqlitevec`: optional scalar L1 extension execution with exact Go ranking over the same SQLite format
- `stores/redis`: atomic snapshots/hash generations, dense exact search and opt-in HNSW/FLAT
- `stores/qdrant`: bounded standalone snapshots and exact Go search ([scope and limits](docs/qdrant.md))
- `stores/memory`: transient model collections for development and contract tests
- `connectors/azuresearch` and `connectors/pinecone`: independently versioned native adapters. See the [native connector support matrix](docs/native-support.md) for tested operations across all backends.
- `samples`: runnable demos (see `samples/README.md`)
- `docs`: architecture and implementation notes

## Install

```bash
go get github.com/gabisonia/go-vectorstore@latest
```

Use the [release list](https://github.com/gabisonia/go-vectorstore/releases) to
check available tags and pin the resolved version in go.mod.
Master can include later changes; use a matching tag for a released checkout. See the
[upgrade guide](docs/upgrading.md) and [compatibility policy](docs/releases.md).

## Run the repository samples

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
aspire start --apphost samples/aspire-services/apphost.go
aspire wait postgres --apphost samples/aspire-services/apphost.go
```

2. Export your OpenAI key and run the semantic sample:

```bash
export OPENAI_API_KEY=your_key_here
go run ./samples/semantic-search -q "how can I reduce cloud costs?"
```

3. Stop local services when done:

```bash
aspire stop --apphost samples/aspire-services/apphost.go --force
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
Model callers can use `InsertOne`/`UpsertOne`/`DeleteOne` and `TryGet` for
[single-record workflows](samples/recipes/README.md). Batch operations remain
available for bulk writes. Original `Record`/`Collection` callers remain compatible.
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

Run unit tests with `go test ./...`. For the PostgreSQL provider and recipes,
run each package separately against a disposable PostgreSQL database:

```sh
go test -count=1 -race -tags=integration -timeout=5m ./stores/postgres
go test -count=1 -race -tags=integration -timeout=5m ./samples/recipes
```

By default these tests use Docker and `pgvector/pgvector:pg16`. Set
`PGVECTOR_TEST_IMAGE=pgvector/pgvector:pg18` to test PostgreSQL 18, or set
`PGVECTOR_TEST_DSN` to use an existing disposable database. Redis and Qdrant
have their own fixtures and test commands. See [Redis verification](docs/redis.md#verification)
and the [Qdrant provider guide](docs/qdrant.md).

## Aspire local development (optional)

The [shared Go AppHost](samples/aspire-services/README.md) manages persistent local
PostgreSQL, Redis and Qdrant services, the semantic-search/RAG examples, and the local
documentation agent. Aspire owns readiness, dependency ordering, logs and terminals.

Use Aspire CLI 17.0.0, Go 1.26+ and Docker. The root library also requires Go 1.26+;
AppHosts and their generated SDKs live in isolated modules. Ordinary AppHost stops
retain persistent services; `--force` removes those containers while preserving
data volumes. Integration tests still use Testcontainers for disposable databases.

For a browser-based vector/hybrid search demo with traces, logs and metrics, run
the separate [Aspire vector-search sample](samples/aspire-vector-search/README.md).

## Release Automation

GitHub Actions workflows are configured for:

- CI on pushes/PRs with race detection, `go vet`, release-script tests, and vulnerability scanning, testing Go 1.26/current stable and PostgreSQL 16/18 (`.github/workflows/ci.yml`)
- release publishing by dispatch from `master` (`.github/workflows/release.yml`), gated on full CI

Release version input is passed through environment variables and validated before
any publishing job runs. Existing tags must point to the checked-out, tested
commit; the workflow will not move a tag to another commit. Prerelease versions
are published as GitHub prereleases.

`VERSION` selects the source version being prepared or tagged (`1.0.0`). It does
not establish that the tag has been published. Choose an unused version and update
`VERSION` and the changelog in a reviewed PR before publishing from master. Published tags are immutable; documentation updates after
a release do not change the source stored under that tag.

Dispatch the `Release` workflow on `master` with the new `version` input, with
or without a leading `v`. Immediately before tagging, the workflow checks that
the tested SHA is still the tip of `master`. See the [release procedure](docs/releases.md).

## Samples

[Search an engineering handbook](samples/handbook/README.md) to see a cited passage change after editing and reindexing its source. Replay it locally with SQLite or on PostgreSQL with pgvector.

- Overview and entry points: [`samples/README.md`](samples/README.md)
- Local agent with Ollama and SQLite: [`local-doc-agent`](samples/local-doc-agent/README.md) (Aspire, separate module)
- Redis model snapshots and exact retrieval: [`redis-store`](samples/redis-store/README.md)
- Typed semantic search and RAG: [`typed-search`](samples/typed-search/README.md), [`typed-rag`](samples/typed-rag/README.md)
- Focused model workflows: [`recipes`](samples/recipes/README.md)
- Generated typed filters and ordering: [`typed-fields`](samples/typed-fields/README.md)
- Model-driven collections (no API key): [`samples/model-store`](samples/model-store/README.md)
- Persistent SQLite and optional vectors (no API key): [`samples/sqlite-store`](samples/sqlite-store/README.md)
- Optional SQLite scalar L1 execution: [`samples/sqlitevec-store`](samples/sqlitevec-store/README.md)
- Reproducible SQLite workload measurements: [`samples/sqlite-workload`](samples/sqlite-workload/README.md) (small default, explicit larger matrix)
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
- Opt-in sqlite-vec and precision proof: [`docs/sqlitevec.md`](docs/sqlitevec.md)
- Redis provider: [`docs/redis.md`](docs/redis.md)

- Provider and .NET parity: [`docs/parity.md`](docs/parity.md)
- Upgrade guide: [`docs/upgrading.md`](docs/upgrading.md)
- Model migrations: [`docs/migrations.md`](docs/migrations.md)
- Changelog: [`CHANGELOG.md`](CHANGELOG.md)
- Benchmarks and performance notes: [`docs/performance.md`](docs/performance.md)

Redis defaults to compatible snapshot storage. Opt-in RedisJSON uses immutable
JSON documents with precise canonical payloads and explicit snapshot/hash conversion.
Opt-in native indexing supports HNSW/FLAT vector search on immutable HASH/JSON
generations, preserving atomic batches. Both layouts accelerate supported scalar
filters and create TEXT fields from string full-text hints. Native dense hybrid
fuses plain-text keyword/vector ranks with RRF from one guarded generation.
Unsupported filters keep exact Go evaluation. KeywordTerms lists OR plain-text
AND groups; native mode advertises HybridKeywordTerms.
Native indexing requires Redis Search and database zero; writes rebuild the generation's index.
See [native support and fallback rules](docs/redis.md#native-indexes-and-search).

## Contributing

[Start with the contributor guide](CONTRIBUTING.md) for a first issue, a local test command, and how to submit a pull request.

## License

This project is licensed under the MIT License.
