# Using go-vectorstore with a coding agent

This guide is for applications importing the library. Repository contributors
should also read [AGENTS.md](../AGENTS.md). Give your coding agent this guide and
the relevant recipe/source files explicitly; installing a Go module does not
ensure that a tool reads its documentation or instruction files.

## Version and starting point

The published release is **v0.4.0**. Install it with:

```sh
go get github.com/gabisonia/go-vectorstore@v0.4.0
```

Go 1.25+ is required. Check your application's `go.mod` before choosing APIs.
Master includes unreleased changes listed in [CHANGELOG.md](../CHANGELOG.md).
The new typed-search, typed-rag and recipes commands are on master and will appear
in the next tag; their library calls use v0.4.0 APIs. Run repository samples from a
checkout of the matching source, rather than assuming they exist in every tag.
[Versioned API reference](https://pkg.go.dev/github.com/gabisonia/go-vectorstore/vectordata@v0.4.0)
and [upgrade notes](upgrading.md) describe the released contract.

Use the model API for new applications. Import the provider you need and
`github.com/gabisonia/go-vectorstore/vectordata`. The original PostgreSQL Record
API is supported and has its own record/filter/options types.

## Minimal complete program

This program needs no database server or API key. The vectors are instructional.
Its behavior is also exercised by [Example_typedSearch](../vectordata/example_test.go).

```go
package main

import (
    "context"
    "fmt"

    "github.com/gabisonia/go-vectorstore/stores/memory"
    "github.com/gabisonia/go-vectorstore/vectordata"
)

func main() {
    type Document struct {
        ID       string    `vectordata:"key"`
        Category string    `vectordata:"data,index"`
        Vector   []float32 `vectordata:"vector,dimensions=2"`
    }
    ctx := context.Background()
    documents, err := vectordata.EnsureTypedModelCollection[string, Document](
        ctx, memory.New(), "docs", nil, vectordata.ModelCollectionOptions{})
    if err != nil {
        panic(err)
    }
    _, err = documents.Upsert(ctx, []Document{
        {ID: "guide", Category: "travel", Vector: []float32{1, 0}},
        {ID: "news", Category: "news", Vector: []float32{0, 1}},
    })
    if err != nil {
        panic(err)
    }
    hits, err := documents.Search(ctx, []float32{1, 0}, 2,
        vectordata.ModelSearchOptions{
            Filter: vectordata.Eq(vectordata.Column("Category"), "travel"),
        })
    if err != nil {
        panic(err)
    }
    for _, hit := range hits {
        fmt.Println(hit.Item.ID, hit.Distance, len(hit.Item.Vector))
    }
}
```

Expected output: `guide 0 0`. The final zero demonstrates that vector projection
is off by default. `scripts/check-docs.py` compiles/runs this fenced program as
part of CI. Replace panic with returned/handled errors in application code.

## Choose a workflow

| Task | Working source | Key calls |
| --- | --- | --- |
| Typed CRUD and generated keys | [model-store](../samples/model-store/main.go) | EnsureTypedModelCollection, Upsert, GetMany, Delete |
| Persistent embedded data | [sqlite-store](../samples/sqlite-store/main.go), [SQLite example](../stores/sqlite/example_test.go) | sqlite.Open, GetTypedModelCollection, Get |
| Automatic embeddings and filtered retrieval | [typed-search](../samples/typed-search/main.go), [model and adapters](../samples/internal/demo/demo.go) | source tag, Embedder, SearchText, Column |
| Retrieve sources for generation | [typed-rag](../samples/typed-rag/main.go) | SearchText, TypedModelSearchResult.Item |
| Named vectors and projections | [recipes](../samples/recipes/main.go), [executable examples](../vectordata/example_test.go) | VectorProperty, IncludeVectors |
| Early iteration/cancellation | [recipes](../samples/recipes/main.go) | QueryIter, context.WithCancel, errors.Is |
| Check backend support | [recipes](../samples/recipes/main.go), [capability contract](../vectordata/capabilities.go) | CapabilityProvider.Capabilities |
| Keyword/vector fusion | [hybrid recipe](../samples/recipes/README.md) | HybridSearchText, HybridSearchOptions |
| Inspect schema changes | [migration recipe](../samples/recipes/README.md), [migration guide](migrations.md) | MigrationPlanner.PlanMigration |
| Runtime/dynamic schemas | [model API](model-api.md) | CollectionDefinition, ModelRecord, ModelStore |

The sample-only `samples/internal/demo` package is internal to this repository.
Applications should copy/adapt the relevant setup or implement their own adapter;
it is not an importable public helper package.

## Rules that prevent common integration mistakes

- Match `TypedModelCollection[K,T]`'s key type to the model key: string, int64 or
  normalized UUID string. Generated int64 keys are returned from writes; do not
  assume the input struct is modified with its generated ID.
- Use `Column("Category")` for logical model fields and
  `DataPath("Details", "rating")` for a JSON data field. `Metadata(...)` belongs
  to the original Record API. Filters are AST nodes, not SQL strings.
- Declare dimensions and the intended metric. Dense fields are `[]float32`,
  including float16 storage. Cosine rejects zero vectors; all inputs must be
  finite. Missing optional vectors are omitted from search on that property.
- Use the same embedding model, configuration and dimensions for writes and
  queries. The library validates shapes but does not persist/verify embedding
  model identity. Changing a model can require re-embedding even at the same
  dimensions. Give incompatible fixtures/models separate collections.
- An Embedder receives a batch of texts and must return one vector per input in
  input order, honoring context cancellation. Configure `SourceProperty` or a
  `source=Body` tag for automatic write embeddings and pass an Embedder through
  store/collection/property options. `EmbeddingBatchSize` bounds text counts;
  model token limits remain the adapter/application's responsibility.
- Specify `VectorProperty` when the model has multiple vectors. Use the explicit
  SearchBinary/SearchSparse interfaces for binary/sparse queries. Request
  IncludeVectors explicitly when a read/search needs stored vector values.
- `top` must be positive, Skip nonnegative, and their sum must not overflow.
  Exact memory/SQLite search scans the collection; deep paging retains more
  candidates. Approximate PostgreSQL searches may return fewer than top.
- Lower Distance is closer; higher Score is better. DistanceThreshold is an
  upper distance bound; ScoreThreshold is a lower similarity bound. Hybrid
  results use ScoreRRF and cannot be compared directly with similarity scores.
- Get returns ErrNotFound for a missing key. GetMany omits missing keys and makes
  no ordering guarantee. Check errors with errors.Is/errors.As; PostgreSQL and
  SQLite may wrap them in OperationError. See the executable error example.
- Check each iterator error and check ctx.Err before expensive per-item work:
  drivers may have buffered rows before cancellation. Breaking a PostgreSQL
  iterator releases its cursor;
  memory/SQLite materialize query results or search pages before yielding.
  Propagate request contexts and close the owning SQLite Store/pgx pool.
- Capabilities describe implementation support, not deployment health. Assert
  optional interfaces as well. Memory/SQLite accept index hints but have no
  physical ANN/full-text indexes, hybrid search, or migration support.
- Ensure registers or validates a schema; it does not migrate an incompatible
  model. Plan PostgreSQL changes, inspect Issues, SQL, Args and risk flags, then
  explicitly apply a reviewed supported plan when the application intends it.
  A migration preview must not call ApplyMigration or execute emitted SQL.

## Suggested task context for an agent

Copy this and fill in your application choices:

```text
Use go-vectorstore v0.4.0 and the typed model API.
Provider: SQLite. Key: string. Model: Document with Body and a 1536-dimensional vector.
Embedder: my application's existing implementation; source property: Body.
Read docs/ai-guide.md and the typed-search/SQLite examples first.
Implement upsert and filtered SearchText with a request context and returned errors.
Keep collection schema and embedding configuration explicit.
Verify with a deterministic embedder and a temporary database; do not call a paid API.
Run go test ./... and go vet ./... in this application.
```

Adapt the model/dimensions to your actual embedder. The library does not implement
an MCP server or automatically register retrieval tools with an agent framework.
An application can expose its retrieval function as a tool while retaining its
own authorization and data boundaries.
