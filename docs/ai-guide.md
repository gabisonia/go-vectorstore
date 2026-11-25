# Using go-vectorstore with a coding agent

This guide is for applications importing the library. Repository contributors
should also read [AGENTS.md](../AGENTS.md). Give your coding agent this guide and
the relevant recipe/source files explicitly; installing a Go module does not
ensure that a tool reads its documentation or instruction files.

## Version and starting point

This quickstart uses the published **v1.0.0** root module and requires Go 1.26
or later. Start in an empty directory. If you already have a Go module, skip
the initialization line.

```sh
go mod init example.com/vector-demo
go get github.com/gabisonia/go-vectorstore/stores/memory@v1.0.0
```

Applications using Go 1.25 can use v0.5.0. Run repository samples from the
matching checkout and check isolated samples' own `go.mod` files. Azure AI Search
and Pinecone have separate module paths and versions. See the [v1.0.0 API
reference](https://pkg.go.dev/github.com/gabisonia/go-vectorstore@v1.0.0/vectordata)
and [upgrade guide](upgrading.md).

Use the model API for new applications. Import the provider you need and
`github.com/gabisonia/go-vectorstore/vectordata`. The original PostgreSQL Record
API is supported and has its own record/filter/options types.

## Minimal complete program

Save the program as `main.go` and run `go run .`. It uses hand-written vectors
and needs no server or API key.
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
is off by default. CI checks this output against both the checkout and a fresh
consumer module using the install commands above. The install pin must match
the published release named in this guide. Handle errors in application code.

## Choose a workflow

| Task | Working source | Key calls |
| --- | --- | --- |
| Single-record CRUD and optional lookup | [recipes](../samples/recipes/main.go) | InsertOne, UpsertOne, TryGet, DeleteOne |
| Typed CRUD and generated keys | [model-store](../samples/model-store/main.go) | EnsureTypedModelCollection, Upsert, GetMany, Delete |
| Persistent embedded data | [sqlite-store](../samples/sqlite-store/main.go), [SQLite example](../stores/sqlite/example_test.go) | sqlite.Open, GetTypedModelCollection, Get |
| Redis collection snapshots | [redis-store](../samples/redis-store/README.md) | redis.New, exact dense search in Go; whole-collection transfers |
| Automatic embeddings and filtered retrieval | [typed-search](../samples/typed-search/main.go), [model and adapters](../samples/internal/demo/demo.go) | source tag, Embedder, SearchText, Column |
| Bounded standalone Qdrant models | [Qdrant sample](../samples/qdrant-store/README.md) | Exact Go search; finite snapshot/receipt limits |
| Local agent with model-selected tools | [Aspire demo](../samples/local-doc-agent/README.md) | Agent Framework function tools, SQLite, Ollama |
| Custom/multimodal embedding inputs | [input-embeddings](../samples/input-embeddings/main.go) | InputEmbeddingFunc, vector,input tag, SearchInput |
| Retrieve sources for generation | [typed-rag](../samples/typed-rag/main.go) | SearchText, TypedModelSearchResult.Item |
| Named vectors and projections | [recipes](../samples/recipes/main.go), [executable examples](../vectordata/example_test.go) | VectorProperty, IncludeVectors |
| Read iteration/cancellation | [recipes](../samples/recipes/main.go) | ListCollectionNamesIter, GetManyIter, SearchTextIter, SearchInputIter, context.WithCancel |
| Check backend support | [recipes](../samples/recipes/main.go), [capability contract](../vectordata/capabilities.go) | CapabilityProvider.Capabilities |
| Keyword/vector fusion | [hybrid recipe](../samples/recipes/README.md) | HybridSearchTextIter, HybridSearchOptions |
| Inspect schema changes | [migration recipe](../samples/recipes/README.md), [migration guide](migrations.md) | MigrationPlanner.PlanMigration |
| Runtime/dynamic schemas | [model API](model-api.md) | CollectionDefinition, ModelRecord, ModelStore |

The sample-only `samples/internal/demo` package is internal to this repository.
Applications should copy/adapt the relevant setup or implement their own adapter;
it is not an importable public helper package.

## Rules that prevent common integration mistakes

- Match `K` in `TypedModelCollection[K,T]` to the model's key field. Explicit keys
  support strings, compatible UUID types, `int`, `uint`, and their fixed-width
  counterparts, including named integer types such as `type UserID int32`. For generated integer
  keys, both the field and `K` must be signed 64-bit types. For generated integer
  or UUID keys, assign the key returned by the write to your model before reusing it.
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
  Deep pages retain more candidates in memory and SQLite. PostgreSQL vector
  search can return either record when equal distances straddle a page boundary.
  Approximate searches can also return fewer than `top` matches.
- Lower Distance is closer; higher Score is better. DistanceThreshold is an
  upper distance bound; ScoreThreshold is a lower similarity bound. Hybrid
  results use ScoreRRF and cannot be compared directly with similarity scores.
- Get returns ErrNotFound for a missing key. TryGet returns value/found/error;
  provider absence is false/nil, while codec and collection errors remain errors.
  GetMany omits missing keys and makes
  no ordering guarantee. Check errors with errors.Is/errors.As; PostgreSQL and
  SQLite may wrap them in OperationError. See the executable error example.
- Check each iterator error and check ctx.Err before expensive per-item work:
  drivers may have buffered rows before cancellation. Breaking a PostgreSQL
  iterator releases its cursor;
  memory/SQLite materialize query results or search pages before yielding.
  New model read iterators are lazy, rerun on enumeration, and may yield a late
  error after earlier records. Package helpers adapt older slice-only providers.
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
Use the latest published go-vectorstore release compatible with the application and the typed model API.
Pin the resolved version in go.mod.
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
