# go-vectorstore

[![Go Reference](https://pkg.go.dev/badge/github.com/gabisonia/go-vectorstore.svg)](https://pkg.go.dev/github.com/gabisonia/go-vectorstore)
[![Latest Release](https://img.shields.io/github/v/release/gabisonia/go-vectorstore?sort=semver)](https://github.com/gabisonia/go-vectorstore/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gabisonia/go-vectorstore)](https://github.com/gabisonia/go-vectorstore/blob/master/go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A lightweight Go vector-store library inspired by `Microsoft.Extensions.VectorData`.

MVP scope:
- Go 1.22+
- Postgres + `pgvector` only
- Record-based core API with optional typed codec wrapper

## Project layout

- `vectordata`: backend-agnostic core interfaces, record model, filters, typed wrapper
- `stores/postgres`: Postgres implementation with `pgxpool`

## Documentation

- Detailed internals and architecture: `docs/README.md`

## Install

```bash
go get github.com/gabisonia/go-vectorstore
```

## Connect

```go
package main

import (
    "context"

    "github.com/gabisonia/go-vectorstore/stores/postgres"
    "github.com/jackc/pgx/v5/pgxpool"
)

func main() {
    ctx := context.Background()
    pool, err := pgxpool.New(ctx, "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
    if err != nil {
        panic(err)
    }
    defer pool.Close()

    store, err := postgres.NewVectorStore(pool, postgres.DefaultStoreOptions())
    if err != nil {
        panic(err)
    }

    _ = store
}
```

## Ensure a collection

```go
collection, err := store.EnsureCollection(ctx, vectordata.CollectionSpec{
    Name:      "docs",
    Dimension: 3,
    Metric:    vectordata.DistanceCosine,
    Mode:      vectordata.EnsureStrict,
})
if err != nil {
    panic(err)
}
```

## Upsert records

```go
records := []vectordata.Record{
    {
        ID:      "doc-1",
        Vector:  []float32{0.1, 0.2, 0.3},
        Metadata: map[string]any{"category": "news", "rank": 10},
    },
    {
        ID:      "doc-2",
        Vector:  []float32{0.2, 0.1, 0.2},
        Metadata: map[string]any{"category": "blog", "rank": 5},
    },
}
if err := collection.Upsert(ctx, records); err != nil {
    panic(err)
}
```

## Search with filter

```go
filter := vectordata.And(
    vectordata.Eq(vectordata.Metadata("category"), "news"),
    vectordata.Gt(vectordata.Metadata("rank"), 6),
)

results, err := collection.SearchByVector(ctx, []float32{0.1, 0.2, 0.25}, 5, vectordata.SearchOptions{
    Filter: filter,
})
if err != nil {
    panic(err)
}

for _, r := range results {
    println(r.Record.ID, r.Distance, r.Score)
}
```

## Ensure indexes

```go
err = collection.EnsureIndexes(ctx, vectordata.IndexOptions{
    Vector: &vectordata.VectorIndexOptions{
        Method: vectordata.IndexMethodHNSW,
        Metric: vectordata.DistanceCosine,
        HNSW: vectordata.HNSWOptions{
            M:              16,
            EfConstruction: 64,
        },
    },
    Metadata: &vectordata.MetadataIndexOptions{
        UsePathOps: true,
    },
})
if err != nil {
    panic(err)
}
```

## Integration tests

```bash
go test -tags=integration ./...
```

Unit tests run with:

```bash
go test ./...
```

Notes:
- Integration tests start Postgres + pgvector automatically via Testcontainers.
- Docker daemon must be available when running integration tests.
- Optional override: set `PGVECTOR_TEST_DSN` to use an existing Postgres instance instead of starting a container.

## Docker Compose (optional)

`docker-compose.yml` at the repository root is kept for manual local runs.

- Use root `docker-compose.yml` when you want a persistent local Postgres+pgvector instance outside tests.
- Use Testcontainers (`go test -tags=integration ./...`) for integration tests.
- Sample app has its own compose file at `samples/semantic-search/docker-compose.yml`.

## Samples

- Semantic search sample app: `samples/semantic-search`

## License

This project is licensed under the MIT License.
