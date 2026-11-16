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
| Native keys and data | string/int64/uint64/UUID keys; 20 data types, including exact decimal/calendar values and arrays | Implemented; inspect capabilities |
| Collection lifecycle and CRUD | ModelStore, CollectionManager, ModelCollection; batch operations and generated int64/UUID keys | Implemented |
| Vector selection and retrieval | Multiple named properties, optional projection, nullable vectors | Implemented |
| Dense, half, binary, sparse search | Search/SearchBinary/SearchSparse; explicit metric, thresholds and paging | Implemented; index support varies |
| Squared Euclidean distance | DistanceL2Squared (`l2_squared`), squared-distance thresholds and reciprocal similarity scores | All three providers; dense float32/float16 and sparse |
| Automatic embeddings | Text/custom-input embedders, direct input fields, store/collection/property overrides, bounded batches, SearchText/SearchInput | Implemented for dense outputs |
| Filtered retrieval/search | Portable AST, JSON paths, native arrays, Contains/ContainsAny/ContainsAll | Implemented |
| Read iteration and cancellation | Listing, batch reads, text/custom-input and every supported hybrid variant use iter.Seq2/context.Context | Implemented; PostgreSQL cursor enumeration, memory/SQLite snapshots/pages; compatibility fallbacks buffer slices |
| Hybrid keyword/vector search | Optional HybridSearcher and binary/sparse variants, RRF | PostgreSQL |
| Provider discovery | CapabilityProvider reports keys, types, vector kinds, physical index tuples and optional workflows | Implemented |
| Schema evolution | Read-only plans, drift detection, explicit reviewed additive application | PostgreSQL |
| Durable provider portability | PostgreSQL and embedded SQLite | Implemented; memory is transient |

## Provider matrix

| Capability | PostgreSQL | SQLite | Memory |
| --- | --- | --- | --- |
| Persistence | Server tables | Local database file | Process lifetime |
| Model keys, all native data types | Yes | Yes | Yes |
| Dense/float16/binary/sparse and optional vectors | Yes | Yes | Yes |
| Exact search | Yes | Yes, snapshot scan | Yes, snapshot scan |
| Physical vector indexes | HNSW/IVFFlat, supported tuples | No | No |
| Native data indexes | B-tree/GIN by property type | No | No |
| Full-text and hybrid search | Yes | No | No |
| Migration planning/application | Yes, additive subset | No | No |
| Original Record API | Yes | No | No |

Use Capabilities on the concrete store and optional interface assertions at
runtime; do not infer ANN/index support from accepted schema hints. PostgreSQL
also depends on deployed pgvector version, permissions, index dimensions and NNZ
limits. See [model API](model-api.md), [SQLite costs](sqlite.md), and
[migration limits](migrations.md).

## Deliberate differences and future extensions

Go uses interfaces, generics, tags, explicit filter nodes, iterators and contexts
in place of .NET abstract classes, attributes, expression trees, async enumerables
and cancellation tokens. Native key/data support is a defined portable subset.
Embedding input supports text, media bytes and custom values through application
adapters; output is dense []float32. Provider-side generation and automatic
binary/sparse embedding outputs remain outside the current contract.
Runtime service discovery comparable to
[VectorStore.GetService](https://learn.microsoft.com/en-us/dotnet/api/microsoft.extensions.vectordata.vectorstore)
is represented by concrete providers and optional interface assertions.

Additional remote connectors, other distance metrics, custom rerankers, SQLite
ANN/full-text acceleration, and non-additive migration execution are possible
extensions. They are not required for the model workflows marked implemented.
