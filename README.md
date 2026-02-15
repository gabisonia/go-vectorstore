# go-vectorstore

[![Go Reference](https://pkg.go.dev/badge/github.com/gabisonia/go-vectorstore.svg)](https://pkg.go.dev/github.com/gabisonia/go-vectorstore)
[![Latest Release](https://img.shields.io/github/v/release/gabisonia/go-vectorstore?sort=semver)](https://github.com/gabisonia/go-vectorstore/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gabisonia/go-vectorstore)](https://github.com/gabisonia/go-vectorstore/blob/master/go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A lightweight Go vector-store library inspired by `Microsoft.Extensions.VectorData`.

MVP scope:
- Go 1.24.x
- Postgres + `pgvector`
- MSSQL connector
- Record-based core API with optional typed codec wrapper

This library can be used to build retrieval systems such as:
- semantic search
- RAG pipelines (retrieve from vector DB, then generate with an LLM)
- context-aware assistants grounded on your own data

## Requirements

- Go 1.24.x
- PostgreSQL 16+ with `pgvector` extension
- SQL Server 2022+ (for `stores/mssql`)
- Docker (optional, for integration tests and sample compose flows)

## Project layout

- `vectordata`: backend-agnostic core interfaces, record model, filters, typed wrapper
- `stores/postgres`: Postgres implementation with `pgxpool`
- `stores/mssql`: SQL Server implementation with `database/sql`
- `samples`: runnable demos (see `samples/README.md`)
- `docs`: architecture and implementation notes

## Install

```bash
go get github.com/gabisonia/go-vectorstore
```

## Quick Start (Local)

1. Start local Postgres + `pgvector` from the repository root:

```bash
docker compose up -d postgres
```

2. Export your OpenAI key and run the semantic sample:

```bash
export OPENAI_API_KEY=your_key_here
go get github.com/gabisonia/go-vectorstore/vectordata@latest
go run ./samples/semantic-search -q "how can I reduce cloud costs?"
```

3. Stop local services when done:

```bash
docker compose down
```

## Core API Walkthrough

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

## Search Options

`SearchByVector` supports filtering, thresholding, and projection control.

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
- Integration tests start Postgres/pgvector and SQL Server automatically via Testcontainers
- Docker daemon must be available when running integration tests
- Optional override: set `PGVECTOR_TEST_DSN` to use an existing Postgres instance instead of starting a container
- Optional override: set `MSSQL_TEST_DSN` to use an existing SQL Server instance instead of starting a container

Run MSSQL integration tests against root compose service:

```bash
docker compose up -d mssql
MSSQL_TEST_DSN="sqlserver://sa:YourStrong%21Passw0rd@localhost:14339?database=master&encrypt=disable" \
  go test -tags=integration ./stores/mssql
```

## Docker Compose (optional)

`docker-compose.yml` at the repository root is kept for manual local runs.

- Use root `docker-compose.yml` when you want a persistent local Postgres+pgvector instance outside tests
- Root compose also includes SQL Server (`mssql`) for local MSSQL connector validation
- Use Testcontainers (`go test -tags=integration ./...`) for integration tests
- Sample apps have their own compose files at `samples/semantic-search/docker-compose.yml` and `samples/ragrimosa/docker-compose.yml`
- Sample Dockerfiles use `golang:1.24-alpine` to match the repo Go version (`1.24.x`)

## Release Automation

GitHub Actions workflows are configured for:

- CI on pushes/PRs (`.github/workflows/ci.yml`)
- release publishing (`.github/workflows/release.yml`)

Release options:

1. Manual (recommended): run `Release` workflow via GitHub UI with `version` input (`0.2.2` or `v0.2.2`).
2. Tag-driven: push a semver tag and the workflow publishes release notes automatically:

```bash
git tag v0.2.2
git push origin v0.2.2
```

## Samples

- Overview and entry points: [`samples/README.md`](samples/README.md)
- Semantic search sample app: `samples/semantic-search`
- RAG sample app (`RAGrimosa`): `samples/ragrimosa`

## Documentation

- Docs index: [`docs/README.md`](docs/README.md)
- Internals and architecture: [`docs/architecture.md`](docs/architecture.md)

## License

This project is licensed under the MIT License.
