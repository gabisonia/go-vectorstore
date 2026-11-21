# Pre-v1 compatibility review

This review uses published v0.6.0 as its API baseline. The commitments below already apply within a supported minor line under the [release policy](releases.md). The decisions below define the scope to verify before v1.

## Public surface and evidence

The [signature check](../scripts/check-api.sh) covers exported packages, including native connectors. For the exact declarations in a checkout, run `go doc -all ./vectordata` and `go doc -all ./stores/<provider>`, substituting the provider directory. The following inventory groups declarations by what an application relies on.

| Surface | Observable behavior to preserve | Representative evidence |
| --- | --- | --- |
| Record API | Keep the string ID, dense vector, metadata and optional content, the VectorStore and Collection interfaces, CRUD, filtering, vector search, projection and index options. Default search projection includes metadata and content and omits the vector. | [Types and defaults](../vectordata/types.go), [examples](../vectordata/example_test.go) |
| Model API | CollectionDefinition describes keys, data and named vectors. Dynamic ModelRecord, typed collections and custom codecs share the declared key type, nullability and retrieval semantics. Preserve exact integer keys and the distinction between missing vectors and zero vectors. | [Model reference](model-api.md), [integer keys](../vectordata/conformance/integer_keys.go), [optional vectors](../vectordata/conformance/optional_vectors.go) |
| Writes and generated keys | For portable model operations, a rejected batch must leave both records and generated-key allocation unchanged. Errors after sending a remote write can leave its outcome unknown. Retain documented error matching and any provider-specific confirmation mechanism. | [Model conformance](../vectordata/conformance/model.go), [sequence rollback](../stores/memory/keys_test.go), [operation errors](../vectordata/operation_error_test.go), [Qdrant outcomes](qdrant.md) |
| Retrieval, query, iterator and filter APIs | Preserve explicit sort and paging semantics, key ordering for exact search ties, SQL-style unknown truth in portable filters and context cancellation through errors.Is. A cancelled iterator must stop yielding buffered results. | [Filter parity](../vectordata/conformance/filter_parity.go), [iterator conformance](../vectordata/conformance/iterators.go), [model guide](model-api.md) |
| Vector and hybrid search | Preserve declared distance units, score kind, thresholds and paging. For example, squared L2 returns 25 for a (3,4) displacement and similarity 1/26. Hybrid and native ranking use their documented score contracts. | [Squared L2 conformance](../vectordata/conformance/l2_squared.go), [keyword groups](../vectordata/conformance/hybrid_keywords.go), [provider parity](parity.md) |
| Embedding, schema and capability APIs | Preserve typed input/output conversion, overrides and validation, collection metadata, provider annotations, and the meaning of advertised capabilities. Support varies with provider, topology and selected storage mode. | [Embedding outputs](embedding-outputs.md), [annotation conformance](../vectordata/conformance/annotations.go), [capability conformance](../vectordata/conformance/capabilities.go) |

## Provider options and persistence

| Provider surface | Compatibility boundary | Evidence or limit |
| --- | --- | --- |
| Memory | Store options configure default embeddings and batching. Model semantics apply for the lifetime of the store. | [Memory contract tests](../stores/memory/store_test.go) |
| SQLite and sqlitevec | Preserve readable canonical registrations, values and generated keys. Scalar indexes, packed vectors and native execution are explicit options. Existing collections require explicit rebuild where documented. | [Released v0.4.0 SQLite database](../stores/sqlite/testdata/v0.4.0/format1.db), [reader tests](../stores/sqlite/compatibility_test.go), [extension limits](sqlitevec.md) |
| PostgreSQL | Schema selection, extension creation, strict checks, index/search options and embedding defaults affect an application. Migration planning is read-only and application is explicit. Physical plans and approximate recall depend on the installed backend. | [Migrations](migrations.md), [logical restore test](../stores/postgres/restore_integration_test.go) |
| Redis | Namespace, collection byte limit, storage mode and native indexes determine which layouts can be opened or created. Preserve explicit conversion between snapshot, hash and JSON layouts. | [Format-1 fixture provenance](../stores/redis/testdata/README.md), [layout and upgrade contracts](redis.md), [recovery tests](../stores/redis/recovery_integration_test.go) |
| Qdrant | Preserve namespace ownership checks, persisted limits, bounded snapshots and retained acknowledgement evidence within the documented standalone topology. | [Provider contract](qdrant.md). The separate multi-point experiment is tracked in [#104](https://github.com/gabisonia/go-vectorstore/issues/104). |
| Native connectors | Native schema, connection, authentication and search options belong to the individual adapter. The shared-storage fixtures cover PostgreSQL, SQLite, Redis, Qdrant, SQL Server, Azure AI Search, Azure DocumentDB, Cosmos NoSQL, Oracle, Elasticsearch, MongoDB, Couchbase, Pinecone, Chroma, Milvus and Weaviate. Preserve each documented attachment boundary, score convention and error contract. | [Per-connector versions, commands and known gaps](../internal/parity/README.md). Hosted DocumentDB recovery evidence is tracked in [#105](https://github.com/gabisonia/go-vectorstore/issues/105). |

## Approved v1 scope and release gates

Applications using supported v0.6.0 storage layouts must have a documented path to v1. A layout may require an explicit migration. Preserve direct reads for the existing SQLite v0.4.0 database and Redis format-1 snapshot. Before tagging v1, verify each promised path against data written by its released producer and publish the direct-read and migration matrix. [Storage upgrade fixtures (#112)](https://github.com/gabisonia/go-vectorstore/issues/112)

Commit to native behavior for explicitly listed adapter, backend version and topology combinations with CI coverage of the promised operations. Move experimental connector APIs into independently versioned v0 modules before tagging the root module v1. Where a package contains both a core provider and native attachment APIs, review those surfaces separately. The [native support matrix](native-support.md) records the selected combinations and experimental modules. The [upgrade guide](upgrading.md) describes import migration and release ordering.

Intentional breaking changes belong in a reviewed pre-v1 minor release with release notes and upgrade instructions. Existing v0.6.x patch releases retain the published compatibility policy. Release timing is a separate decision.
