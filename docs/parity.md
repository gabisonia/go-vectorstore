# Model API parity checklist

This checklist maps the repository to the model-driven workflows described by
[Microsoft.Extensions.VectorData](https://learn.microsoft.com/en-us/dotnet/ai/conceptual/mevd-library)
and its [data operations guide](https://learn.microsoft.com/en-us/dotnet/ai/vector-stores/manage-data).
It is a workflow comparison, not a claim of API or feature equivalence with every
.NET connector. Provider capabilities differ in both ecosystems.

The [connector interoperability guide](../internal/parity/README.md) lists the
locked C# packages, physical schemas, local Docker commands and live-service
coverage for the unreleased native APIs. The PostgreSQL fixture uses the
existing typed model API. SQL Server, Azure AI Search, Qdrant, SQLite and Redis have explicit entry points
for attaching to externally managed collections.

| Workflow | Go implementation | Status |
| --- | --- | --- |
| Declare key/data/vector properties | Struct tags, explicit CollectionDefinition, StructCodec and custom codecs | Implemented |
| Typed and dynamic records | TypedModelCollection[K,T] and ModelRecord | Implemented |
| Native keys and data | string/int64/uint64/UUID keys, typed integer widths and aliases; 20 data types, including exact decimal/calendar values and arrays | Implemented; inspect canonical storage capabilities |
| Collection lifecycle and CRUD | ModelStore, CollectionManager, ModelCollection; batch and single-record operations, TryGet found result, generated int64/UUID keys | Implemented |
| Vector selection and retrieval | Multiple named properties, optional projection, nullable vectors | Implemented |
| Dense, half, binary, sparse search | Search/SearchBinary/SearchSparse; explicit metric, thresholds and paging | Implemented; index support varies |
| Squared Euclidean distance | DistanceL2Squared (`l2_squared`), squared-distance thresholds and reciprocal similarity scores | Dense float32/float16 on all providers; sparse on memory/SQLite/PostgreSQL |
| Automatic embeddings | Text/custom-input embedders, typed float32/float16-output adapter, direct input fields, overrides, bounded batches, SearchText/SearchInput | Implemented for dense outputs |
| Filtered retrieval/search | Portable AST, JSON paths, native arrays, Contains/ContainsAny/ContainsAll | Implemented |
| Typed filter/order references | Optional vectordata-gen emits typed tagged-model operands over the same AST | Implemented; JSON paths and dynamic references remain runtime validated |
| Read iteration and cancellation | Listing, batch reads, text/custom-input and every supported hybrid variant use iter.Seq2/context.Context | Implemented; PostgreSQL cursor enumeration, memory/SQLite/Redis snapshots/pages; compatibility fallbacks buffer slices |
| Hybrid keyword/vector search | Optional HybridSearcher and binary/sparse variants, RRF; string queries or plain-text KeywordTerms lists | PostgreSQL and native Redis dense hybrid with string/list keywords; inspect HybridKeywordTerms capability |
| Provider discovery | CapabilityProvider reports keys, types, vector kinds, physical index tuples and optional workflows | Implemented |
| Store/collection metadata | Optional metadata interfaces and helpers report system, database, namespace and collection identity; typed wrappers forward discovery | All providers; local configuration snapshots |
| Error diagnostics | OperationError preserves error chains and embeds configured store identity for structured diagnostics | PostgreSQL/SQLite/Redis model wrappers and PostgreSQL migrations; wrapping is optional |
| Schema evolution | Read-only plans, drift detection, explicit reviewed additive application | PostgreSQL |
| Durable provider portability | PostgreSQL, embedded SQLite, Redis and bounded Qdrant snapshots | Implemented with provider-specific durability/lifetime limits; memory is transient |

The 2026-11-17 API audit checked the current Microsoft single-record
[upsert/delete/get workflows](https://learn.microsoft.com/en-us/dotnet/ai/vector-stores/manage-data).
Typed models expose InsertOne/UpsertOne/DeleteOne/TryGet; dynamic models use package
helpers over the unchanged ModelCollection interface. Get retains ErrNotFound;
TryGet returns an explicit found result for provider absence and preserves codec,
connection and collection errors. Generated keys are returned without mutating
caller models. Malformed typed write replies (nil keys or incorrect key counts)
are rejected, with no replay after an uncertain response. Duplicate PostgreSQL
model writes expose the portable ErrInvalidRecord sentinel and native SQLSTATE
23505 together. Shared provider contracts cover these workflows; this is workflow compatibility, not identical .NET naming
or exception/return-value semantics.

Implementation follows the [ordered parity plan](parity-plan.md). Provider
annotations, SQLite vector projections, opt-in scalar L1 execution and broader
workload evidence have merged. The Qdrant compatibility probe/design has merged;
the bounded [publication protocol proof](qdrant-publication-proof.md) also merged.
The [Qdrant provider foundation](qdrant.md) implements bounded standalone canonical
snapshots with exact dense ranking, portable CRUD/filters/embeddings, metadata,
shared conformance and process-restart checks. Native execution and distributed
support remain separate gates.
The [typed output adapter](embedding-outputs.md) adds float32/float16 generator
representations while retaining the existing dense boundary. Optional
[typed property references](typed-fields.md) add compile-checked model fields and
operands over the portable AST. Native service access remains deferred pending a
concrete consumer.

## Provider matrix

| Capability | PostgreSQL | SQLite | Memory | Redis | Qdrant foundation |
| --- | --- | --- | --- | --- | --- |
| Persistence | Server tables | Local database file | Process lifetime | Server persistence policy | Standalone bounded snapshots |
| Model keys, all portable data types | Yes | Yes | Yes | Yes, canonical JSON | Yes, canonical JSON |
| Dense/float16 and optional vectors | Yes | Yes | Yes | Yes | Yes |
| Binary/sparse vectors | Yes | Yes | Yes | No | No |
| Exact search | Yes | Yes, canonical/optional packed scan | Yes, snapshot scan | Yes, Go snapshot scan | Yes, Go snapshot scan |
| Physical vector indexes | HNSW/IVFFlat, supported tuples | No | No | Opt-in HNSW/FLAT, 16 dense tuples | No |
| Native data indexes | B-tree/GIN by property type | Opt-in scalar B-tree | No | Opt-in HASH/JSON scalar TAG / split-integer NUMERIC | No |
| Full-text indexing | Yes | No | No | Opt-in HASH/JSON TEXT for strings | No |
| Hybrid search | Yes | No | No | Native dense/string/list keywords, HASH/JSON | No |
| Migration planning/application | Yes, additive subset | No | No | No | No |
| Original Record API | Yes | No | No | No | No |

Use Capabilities on the concrete store and optional interface assertions at
runtime; do not infer ANN/index support from accepted schema hints. PostgreSQL
also depends on deployed pgvector version, permissions, index dimensions and NNZ
limits. See [model API](model-api.md), [SQLite costs](sqlite.md), and
[migration limits](migrations.md).

SQLite uses its existing primary key index for top-level key equality/`In`
queries and counts, with one read snapshot across batches. Supported scalar
filters, ordering and paging also run in SQL over payload JSON; unsupported
combinations retain full-snapshot evaluation. Opt-in `NativeIndexes` adds scalar
B-tree projections for indexed string/int64/float64/bool fields. New collections
build transactionally; existing collections require explicit `RebuildScalarIndexes`.
Triggers preserve compatibility with older writers. Vector indexes remain absent.
Optional `VectorProjections` adds packed dense storage for unfiltered exact Go
ranking and page-only canonical retrieval. Existing/dirty collections require
explicit `RebuildVectorProjections`; ordinary triggers invalidate readiness for
unaware writers. Filtered/binary/sparse searches keep their canonical scan.
The [sqlite-vec adapter](sqlitevec.md) optionally selects complete scalar-L1
boundary candidates and retains exact Go ranking. Other metrics/shapes keep the
shared provider's paths; extension presence does not add vector index capability.
See [query costs](sqlite.md#query-costs-and-provider-limits),
[scalar measurements](performance.md#sqlite-scalar-query-verification) and
[index verification](performance.md#sqlite-scalar-index-verification).

Redis is available on master after v0.4.1. Default snapshot storage transfers the
whole collection and runs exact dense search/filtering in Go. Its encoded size
limit defaults to 16 MiB. Plain hash storage adds immutable per-record generations
without changing query execution. Opt-in RedisJSON uses format-3 documents with
opaque canonical payloads, preserving numeric precision through RedisJSON/Lua.
Explicit storage conversions preserve atomic batches and generated sequences.

Opt-in `NativeIndexes` requires Redis Search and database zero. It builds HNSW/FLAT
indexes for cosine/L2/squared-L2/inner-product float32/float16 models and accelerates
eligible key/scalar filters on HASH and JSON storage. Unsupported cases retain Go exact evaluation. FLAT exhaustively
remeasures all matching vectors in Go to preserve ordering through float roundoff.
Writes rebuild the full generation's index; native reads still transfer the token manifest.
Native mode also honors string full-text hints with Redis TEXT indexes; index
metadata versions preserve existing scalar/vector-only generations until rebuild
or a native write. JSON native mode builds HNSW/FLAT over separate vector arrays
while canonical payload strings preserve numeric precision. JSON requires RedisJSON
and uses index metadata version 4 for scalar filtering with full integer precision
and NULL truth. Version-3 vector-only indexes retain Go filtering until an explicit
rebuild/native write. JSON index metadata version 5 adds TEXT projections when
full-text hints exist; older version-3/4 schemas keep their fields until explicit
rebuild/native writes. Native Redis supports dense RRF hybrid search with plain
string keywords or KeywordTerms OR groups, shared-generation candidate reads and
Go remeasurement/filtering. Standalone keyword queries and schema migrations remain absent. See
[native support, costs and guarantees](redis.md#native-indexes-and-search).

## Deliberate differences and future extensions

Go uses interfaces, generics, tags, explicit filter nodes, iterators and contexts
in place of .NET abstract classes, attributes, expression trees, async enumerables
and cancellation tokens. Native key/data support is a defined portable subset.
Embedding input supports text, media bytes and custom values through application
adapters; typed float32 or IEEE float16 generator outputs adapt to canonical
dense []float32. Stored precision remains separate. Provider-side generation and automatic
binary/sparse embedding outputs remain outside the current contract.
Runtime service discovery comparable to
[VectorStore.GetService](https://learn.microsoft.com/en-us/dotnet/api/microsoft.extensions.vectordata.vectorstore)
is represented by concrete providers and optional interface assertions. Store and
collection metadata use dedicated optional interfaces, with name-only fallback
for older collection providers; no general-purpose service locator is provided.

Additional remote connectors, other distance metrics, custom rerankers, SQLite
ANN/full-text acceleration, and non-additive migration execution are possible
extensions. They are not required for the model workflows marked implemented.
