# Model API parity checklist

This checklist maps the repository to the model-driven workflows described by
[Microsoft.Extensions.VectorData](https://learn.microsoft.com/en-us/dotnet/ai/conceptual/mevd-library)
and its [data operations guide](https://learn.microsoft.com/en-us/dotnet/ai/vector-stores/manage-data).
It is a workflow comparison, not a claim of API or feature equivalence with every
.NET connector. Provider capabilities differ in both ecosystems.

| Workflow | Go implementation | Status |
| --- | --- | --- |
| Declare key/data/vector properties | Struct tags, explicit CollectionDefinition, StructCodec and custom codecs | Implemented |
| Typed and dynamic records | TypedModelCollection[K,T] and ModelRecord | Implemented |
| Native keys and data | string/int64/uint64/UUID keys, typed integer widths and aliases; 20 data types, including exact decimal/calendar values and arrays | Implemented; inspect canonical storage capabilities |
| Collection lifecycle and CRUD | ModelStore, CollectionManager, ModelCollection; batch operations and generated int64/UUID keys | Implemented |
| Vector selection and retrieval | Multiple named properties, optional projection, nullable vectors | Implemented |
| Dense, half, binary, sparse search | Search/SearchBinary/SearchSparse; explicit metric, thresholds and paging | Implemented; index support varies |
| Squared Euclidean distance | DistanceL2Squared (`l2_squared`), squared-distance thresholds and reciprocal similarity scores | Dense float32/float16 on all providers; sparse on memory/SQLite/PostgreSQL |
| Automatic embeddings | Text/custom-input embedders, direct input fields, store/collection/property overrides, bounded batches, SearchText/SearchInput | Implemented for dense outputs |
| Filtered retrieval/search | Portable AST, JSON paths, native arrays, Contains/ContainsAny/ContainsAll | Implemented |
| Read iteration and cancellation | Listing, batch reads, text/custom-input and every supported hybrid variant use iter.Seq2/context.Context | Implemented; PostgreSQL cursor enumeration, memory/SQLite/Redis snapshots/pages; compatibility fallbacks buffer slices |
| Hybrid keyword/vector search | Optional HybridSearcher and binary/sparse variants, RRF; string queries or plain-text KeywordTerms lists | PostgreSQL; inspect HybridKeywordTerms capability |
| Provider discovery | CapabilityProvider reports keys, types, vector kinds, physical index tuples and optional workflows | Implemented |
| Store/collection metadata | Optional metadata interfaces and helpers report system, database, namespace and collection identity; typed wrappers forward discovery | All providers; local configuration snapshots |
| Error diagnostics | OperationError preserves error chains and embeds configured store identity for structured diagnostics | PostgreSQL/SQLite/Redis model wrappers and PostgreSQL migrations; wrapping is optional |
| Schema evolution | Read-only plans, drift detection, explicit reviewed additive application | PostgreSQL |
| Durable provider portability | PostgreSQL, embedded SQLite and Redis snapshots | Implemented; Redis durability depends on server policy; memory is transient |

## Provider matrix

| Capability | PostgreSQL | SQLite | Memory | Redis |
| --- | --- | --- | --- | --- |
| Persistence | Server tables | Local database file | Process lifetime | Server persistence policy |
| Model keys, all portable data types | Yes | Yes | Yes | Yes, canonical JSON |
| Dense/float16 and optional vectors | Yes | Yes | Yes | Yes |
| Binary/sparse vectors | Yes | Yes | Yes | No |
| Exact search | Yes | Yes, snapshot scan | Yes, snapshot scan | Yes, Go snapshot scan |
| Physical vector indexes | HNSW/IVFFlat, supported tuples | No | No | Opt-in HNSW/FLAT, 16 dense tuples |
| Native data indexes | B-tree/GIN by property type | No | No | Opt-in scalar TAG / split-integer NUMERIC |
| Full-text indexing | Yes | No | No | Opt-in TEXT for strings |
| Hybrid search | Yes | No | No | No |
| Migration planning/application | Yes, additive subset | No | No | No |
| Original Record API | Yes | No | No | No |

Use Capabilities on the concrete store and optional interface assertions at
runtime; do not infer ANN/index support from accepted schema hints. PostgreSQL
also depends on deployed pgvector version, permissions, index dimensions and NNZ
limits. See [model API](model-api.md), [SQLite costs](sqlite.md), and
[migration limits](migrations.md).

SQLite uses its existing primary key index for top-level key equality/`In`
queries and counts, with one read snapshot across batches. Supported scalar
filters, ordering and paging also run in SQL over payload JSON; unsupported
combinations retain full-snapshot evaluation. Accepted index hints do not add
property or ANN indexes. See [query costs](sqlite.md#query-costs-and-provider-limits)
and [scalar measurements](performance.md#sqlite-scalar-query-verification).

Redis is available on master after v0.4.1. Default snapshot storage transfers the
whole collection and runs exact dense search/filtering in Go. Its encoded size
limit defaults to 16 MiB. Plain hash storage adds immutable per-record generations
without changing query execution. Opt-in RedisJSON uses format-3 documents with
opaque canonical payloads, preserving numeric precision through RedisJSON/Lua.
Explicit storage conversions preserve atomic batches and generated sequences.

Opt-in `NativeIndexes` requires Redis Search and database zero. It builds HNSW/FLAT
indexes for cosine/L2/squared-L2/inner-product float32/float16 models and accelerates
eligible key/scalar filters. Unsupported cases retain Go exact evaluation. FLAT exhaustively
remeasures all matching vectors in Go to preserve ordering through float roundoff.
Writes rebuild the full generation's index; native reads still transfer the token manifest.
Native mode also honors string full-text hints with Redis TEXT indexes; index
metadata versions preserve existing scalar/vector-only generations until rebuild
or a native write. Native indexing currently applies to hash storage; JSON retains
Go filtering/search and requires RedisJSON. Public keyword/hybrid APIs and schema
migrations remain absent. See
[native support, costs and guarantees](redis.md#native-indexes-and-search).

## Deliberate differences and future extensions

Go uses interfaces, generics, tags, explicit filter nodes, iterators and contexts
in place of .NET abstract classes, attributes, expression trees, async enumerables
and cancellation tokens. Native key/data support is a defined portable subset.
Embedding input supports text, media bytes and custom values through application
adapters; output is dense []float32. Provider-side generation and automatic
binary/sparse embedding outputs remain outside the current contract.
Runtime service discovery comparable to
[VectorStore.GetService](https://learn.microsoft.com/en-us/dotnet/api/microsoft.extensions.vectordata.vectorstore)
is represented by concrete providers and optional interface assertions. Store and
collection metadata use dedicated optional interfaces, with name-only fallback
for older collection providers; no general-purpose service locator is provided.

Additional remote connectors, other distance metrics, custom rerankers, SQLite
ANN/full-text acceleration, and non-additive migration execution are possible
extensions. They are not required for the model workflows marked implemented.
