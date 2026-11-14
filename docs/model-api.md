# Model-driven vector stores

The model API follows the main ideas of `Microsoft.Extensions.VectorData`:
application models describe key, data and vector properties; providers use that
same definition for mapping, storage, indexes and search. The original `Record`,
`Collection`, `VectorStore`, and `TypedCollection[T]` APIs remain supported.

## A typed collection

```go
type Product struct {
    ID        string    `vectordata:"key,name=product_id"`
    Name      string    `vectordata:"data,index"`
    Tags      []string  `vectordata:"data,index"`
    Embedding []float32 `vectordata:"vector,dimensions=3,index"`
}

store := memory.New() // Or a postgres.PostgresVectorStore.
products, err := vectordata.EnsureTypedModelCollection[string, Product](
    ctx, store, "products", nil, vectordata.ModelCollectionOptions{},
)
if err != nil {
    return err
}
_, err = products.Upsert(ctx, []Product{{
    ID: "p1", Name: "Quiet keyboard", Tags: []string{"office"},
    Embedding: []float32{1, 0, 0},
}})
if err != nil {
    return err
}
results, err := products.Search(ctx, []float32{1, 0, 0}, 5,
    vectordata.ModelSearchOptions{
        Filter: vectordata.Contains(vectordata.Column("Tags"), "office"),
    })
```

Import `vectordata` and `stores/memory` from this module. See
[`samples/model-store/main.go`](../samples/model-store/main.go) for a complete
program that runs without an API key and can switch to PostgreSQL with `-dsn`.
Its deterministic three-dimensional embedder illustrates the API; use a real
embedding model for semantic search.

`GetTypedModelCollection[K,T]` constructs a handle without database access.
`EnsureTypedModelCollection[K,T]` additionally creates or validates storage.
These are generic functions because Go does not support methods with their own
type parameters. Both struct values and pointers to structs are supported.
Reflection mappings are resolved once when constructing a `StructCodec[T]`.
Custom `ModelCodec[T]` implementations can provide their own mappings.

## Schema definitions and tags

The equivalent explicit `CollectionDefinition` has one `KeyProperty`, a list of
`DataProperty` values, and zero or more `VectorProperty` values. Pass a pointer to
an explicit definition to the typed factory, or pass nil to infer tags.
Definitions are validated and defensively copied. Logical names correspond to Go
field names for struct mapping; `StorageName` controls the database column name.
Logical names must not have surrounding whitespace. Empty/NUL names, duplicate
names, names over 63 bytes, and the reserved `__vs_` prefix are rejected.

| Tag role | Options |
| --- | --- |
| `key` | `name=<storage name>`, `type=string|int64|uuid`, `generated` |
| `data` | `name=<storage name>`, `type=<data type>`, `optional`, `index`, `fulltext` |
| `vector` | `name=<storage name>`, `dimensions=<n>`, `metric=cosine|l2|inner_product`, `index`, `method=hnsw|ivfflat`, `source=<data field>` |
| `-` | Ignore the field |

Untagged fields are ignored when inferring a definition. Exported fields and
exported embedded fields are supported. Invalid tags and type conflicts fail
before storage creation. An explicit definition uses exported Go field names and
does not require tags.

Supported native key types are string, int64 and UUID. UUID keys can be strings
or compatible types with text mapping. PostgreSQL stores these as `text`,
`bigint`, and `uuid`, rather than stringifying all keys into a text column.
`key,generated` requires an int64-compatible field and uses a PostgreSQL identity
column. A zero generated key in a tagged model requests a new key. Insert/Upsert
return keys in input order; input models are not mutated. Assign the returned key
before upserting the same model again. Generated keys can have gaps.

Data types are `string`, `int64`, `float64`, `bool`, `time`, `string_array`, and
`json`. Pointer data fields are optional. Nonoptional fields reject nil values.
JSON numbers retain their precision as `json.Number`. Dynamic records use logical
property names in `ModelRecord.Data` and `ModelRecord.Vectors`.

PostgreSQL uses native columns, B-tree indexes for scalar `index` properties,
GIN indexes for indexed string arrays, GIN full-text indexes for `fulltext`
strings, and optional per-vector HNSW/IVFFlat indexes. JSON properties currently
require provider-specific indexes and reject the generic `index` flag.

Models use strict schema validation: columns, types, nullability, identity and
primary-key shape must match. Existing indexes are checked for conflicts.
Definitions are registered in the provider's reserved `__vectorstore_models`
table; runtime embedder objects are not persisted. Changing a registered model
requires an explicit migration or a new collection. The original Record API's
strict/auto-migrate modes are unchanged.

## Provider capabilities

Built-in stores implement the optional `vectordata.CapabilityProvider` interface.
`Capabilities()` returns a local `ProviderCapabilities` snapshot for the model
API. Applications can inspect supported keys, generated keys, data types,
distance metrics and physical indexes before choosing a model or search flow:

```go
// store is a vectordata.ModelStore. Import fmt and slices from the standard library.
if provider, ok := store.(vectordata.CapabilityProvider); ok {
    caps := provider.Capabilities()
    fmt.Printf("%s: UUID keys=%t, hybrid search=%t\n", caps.Backend,
        slices.Contains(caps.KeyTypes, vectordata.KeyUUID), caps.HybridSearch)
}
```

Both providers report string/int64/UUID keys, generated int64 keys, all seven
portable data types, and cosine/L2/inner-product metrics.

| Feature | Memory | PostgreSQL |
| --- | --- | --- |
| `VectorIndexMethods` | None | HNSW, IVFFlat |
| `IndexedDataTypes` | None | String, int64, float64, bool, time, string array |
| `FullTextIndexing` | False | True, for string properties |
| `HybridSearch` | False | True |

Index fields describe physical acceleration. Memory still accepts index hints
for portability. Generic JSON indexing remains unsupported; provider-specific
indexes are outside this metadata. Empty slices mean no support for that feature.

Discovery does not access the database or check deployment versions, permissions,
health, collection definitions or embedder configuration. A hybrid-capable
provider still needs the relevant model properties and options. Operations retain
their usual schema and deployment validation. Built-in stores return independent,
caller-owned slices; changing a snapshot does not configure the provider.

`ModelStore` remains unchanged. A third-party provider without
`CapabilityProvider` has unavailable metadata; this does not imply its optional
features are unsupported. Callers can still use the `HybridSearcher` interface
and normal operation errors when metadata is unavailable.

## Lifecycle and retrieval

Both providers implement `ModelStore` and `CollectionManager`. Stores can list
collections, check existence, and delete collections idempotently. PostgreSQL
listing includes registered models (even models without vectors) and existing
vector tables in the configured schema. Ordinary unrelated tables are excluded.
`CollectionExists` resolves only the requested, schema-qualified relation rather
than listing every collection. Vector tables need one catalog query; registered
models without vectors also check their registry entry. Views and missing/dropped
relations are excluded even if a stale registry entry exists. Existence checks
do not validate a collection's schema.

| Operation | Behavior |
| --- | --- |
| `Get` | Returns `ErrNotFound` for a missing key |
| `GetMany` | Returns only found records, deduplicates keys, and makes no ordering promise |
| `Query` / `QueryIter` | Filter without vector search, with `top`, `Skip`, and `OrderBy` |
| `Insert` / `Upsert` | Return native keys; all records are committed atomically |
| `Delete` | Ignores missing keys and returns the number deleted |
| `Count` | Applies the same filter language as retrieval/search |

PostgreSQL selects binary vector parameters for requests containing at least
1,024 vector values, and retains text for smaller writes. Both paths keep atomic
writes, generated IDs and returned-key ordering. Driver type mappings are scoped
to the write and restored before pool release. See the
[transport benchmarks](performance.md#binary-vector-ingestion-and-copy-comparison)
for measurements and the COPY comparison.

Reads exclude vectors by default. Use `RetrievalOptions{IncludeVectors:true}`,
`QueryOptions.IncludeVectors`, or `ModelSearchOptions.IncludeVectors` to load
all named vectors. Filtered retrieval sorts by the requested scalar properties
with a key tie-breaker; without explicit ordering it sorts by key. JSON/array
ordering is not part of the portable contract. Text collation can vary by provider.

`QueryIter` and `SearchIter` use Go iterators. PostgreSQL reads rows lazily and
releases the connection on completion, error, cancellation, or early loop exit.
The memory provider snapshots its data. Querying a deleted/missing collection
returns `ErrCollectionNotFound`. PostgreSQL operation errors add backend,
collection, and operation context through `OperationError`, preserving
`errors.Is`/`errors.As`.

## Filtering

Use `Column("Category")` for model properties and `DataPath("Details", "rating")`
for JSON properties. `Metadata(...)` remains the original Record API's JSON path
syntax. Nested JSON keys, including empty/whitespace keys, are literal.

Operators include `Eq`, `Ne`, `In`, `Gt`, `Gte`, `Lt`, `Lte`, `Contains`, `Exists`,
`And`, `Or`, and `Not`. `Contains` means array membership, not substring matching.
String-array predicates use a GIN-compatible PostgreSQL containment expression.

The filter contract preserves SQL-style unknown/null behavior under negation:
a missing JSON path does not match `Eq(path,nil)` or its negation. A present JSON
null does match `Eq(path,nil)`. An optional native JSON column can contain SQL
NULL (`nil`) or JSON null (`JSONNull{}`); `Column` tests native SQL null, while
`DataPath` tests JSON values. `JSONNull{}` is also accepted in JSON path equality
and membership predicates. JSON numbers and strings remain distinct for equality.
Native NULL array membership remains unknown under `Not`, while a missing/nonarray
JSON membership test is false. Invalid fields are rejected even for empty stores.

Ordered JSON comparisons against numbers use guarded numeric conversion;
missing, null and nonnumeric values compare false. Text comparisons preserve SQL
unknown for missing/null paths, including under negation. Array paths support
negative indexes and leading ASCII whitespace; trailing whitespace is invalid.
Object path segments always remain literal keys.

The AST lives in `vectordata/filterast`. PostgreSQL SQL compilation lives in
`stores/postgres/filtersql`. The old `vectordata.CompileFilterSQL` and
`FilterSQLConfig` remain deprecated compatibility shims.

## Named vectors, scores, and paging

Each vector property has its own dimensions, metric, storage name and index.
When multiple vectors exist, specify `ModelSearchOptions.VectorProperty` by its
logical name. An ambiguous selection is an error. All vector elements must be
finite; cosine vectors must be nonzero.

`Skip` and `top` page the ranked results. Approximate indexes can still return
fewer matches or change the candidate set between requests; paging does not
create a stable snapshot across separate searches.

Vector search returns raw `Distance` plus a higher-is-better `Score`, tagged
`ScoreSimilarity`. `DistanceThreshold` is an upper bound on raw distance;
`ScoreThreshold` is a lower bound on the library's similarity score. These are
explicit Go semantics, not interchangeable with every provider-specific .NET
score threshold. HNSW/IVFFlat tuning is passed as
`postgres.SearchTuning` through `ProviderOptions`; unknown provider options are
rejected. Transaction-local settings do not leak to other pooled requests.

## Automatic embeddings

Implement `Embedder.Embed(ctx, inputs []string) ([][]float32,error)` and configure
it in `ModelCollectionOptions`. A `VectorProperty.Embedder` can override the default.
Tagged models connect a vector to a string data field with `source=Description`.

Writes generate missing vectors in one batch per vector property. Explicitly
supplied vectors take precedence. Embedding counts/dimensions and the entire
record batch are checked before writing, so embedding failures leave no partial
records. `SearchText` embeds the query with the selected vector's embedder.
Callers must use compatible embedding models for ingestion and queries;
embedders must honor cancellation and be safe for concurrent use.

## Hybrid search

PostgreSQL implements optional `HybridSearcher`. Declare a string data property
with `IsFullTextIndexed` (or the `fulltext` tag), then call `HybridSearch` or
`HybridSearchText`. Specify `TextProperty` if more than one field is full-text
indexed, and `VectorProperty` if more than one vector is defined. Invalid paging,
thresholds, keywords, properties, filters and local provider settings are rejected
before `HybridSearchText` invokes the embedder; an already-canceled request also
skips embedding.

Keyword search uses PostgreSQL's `simple` text-search configuration and
`plainto_tsquery`. Filters apply to both the vector and keyword candidate sets.
Reciprocal rank fusion combines the rankings with a default constant of 60.
`CandidateLimit` bounds each candidate list and must cover `top + Skip`.
Results have `ScoreKind=ScoreRRF`; their score thresholds apply to fusion scores,
not vector similarity. Raw vector distance is still returned. Fusion scores
should not be compared with ordinary vector similarity scores.

The memory provider performs exact vector search and does not implement hybrid
search. Typed calls return `ErrUnsupported` for this optional capability. Index
hints are accepted for portability but do not create approximate indexes in memory.

## Verification and scope

`vectordata/conformance.Run` exercises shared provider behavior. Both memory tests
and PostgreSQL integration tests run it, including key types, identity keys,
filters, nulls, projections, paging, thresholds, atomicity, and lifecycle.
PostgreSQL-specific tests cover typed mapping, embedding generation, hybrid
search, index conflicts, concurrent initialization and early iterator cleanup.
`conformance.RunFilterParity` compares generated filter trees between two providers;
expected-result edge cases also run as part of `conformance.Run`. See
[performance and verification](performance.md) for benchmark commands, local
measurements and model index-plan checks. Keep production database integration
tests even when using memory for development.

This is a Go adaptation of the main model-driven workflows, not binary or full
feature parity with every .NET provider. Half/binary/sparse vectors, additional
metrics, server-side embedding generation, and automatic model migrations remain
outside the initial model API. Existing Record API callers can adopt models
collection by collection.
