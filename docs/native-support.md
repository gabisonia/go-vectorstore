# Native connector support for v1

The root module retains the following surfaces for v1. Each row scopes support
to the listed backend and operations exercised by required CI. The linked
scripts record image versions or digests, initialization, and the fixture
invocation. Schema shapes, score units, connector versions and known client
differences are documented in the [interoperability guide](../internal/parity/README.md).
Hosted recovery observations there apply to their specific deployments.

| Adapter and backend | Covered native behavior | Required evidence |
| --- | --- | --- |
| Memory | Portable in-process model operations | [Model conformance](../stores/memory/store_test.go) |
| SQLite / sqlitevec, pinned Go SQLite engine and linked extension | Shared cosine table CRUD and search, transactional delete | [SQLite fixture](../scripts/test-reference-catalog.sh) |
| PostgreSQL 16/18 with pgvector | Typed shared-table mapping, CRUD and cosine ordering | [PostgreSQL fixture](../scripts/test-postgres-parity.sh) |
| Redis 8.2 standalone | Attach to hash/JSON search indexes, document CRUD and FLAT cosine ranking | [Redis fixture](../scripts/test-redis-parity.sh) |
| Qdrant 1.20.2 standalone | Named cosine vectors, native IDs, keyword filters and bounded hybrid fixture | [Qdrant fixture](../scripts/test-qdrant-parity.sh) |
| SQL Server 2025 Developer | Shared bigint-keyed vector table, reads, writes and exact cosine distance, typed retrieval with logical names and text equality | [SQL Server fixture](../scripts/test-sqlserver-parity.sh) |
| Weaviate 1.40.7 standalone | UUID object CRUD and named-vector cosine search | [Weaviate fixture](../scripts/test-weaviate-parity.sh) |
| Chroma 1.5.9 | Collection attachment, document updates and native cosine ranking | [Chroma fixture](../scripts/test-chroma-parity.sh) |
| Elasticsearch 8.19.9 | Dense cosine search with a keyword filter and shared document operations | [Elasticsearch fixture](../scripts/test-elasticsearch-parity.sh) |
| Milvus 2.6.27 standalone | VarChar keys, FLAT cosine ranking and filtered reads | [Milvus fixture](../scripts/test-milvus-parity.sh) |
| MongoDB Atlas Local 8.0 | Shared BSON documents and exact vector search with prefilter | [Atlas Local fixture](../scripts/test-mongodb-parity.sh) |
| Couchbase Enterprise 8.0.0 | SQL++ document operations and scoped cosine Search with keyword prefilter | [Couchbase fixture](../scripts/test-couchbase-parity.sh) |
| Oracle Free 23 | Numeric keys, nullable categories, exact cosine search | [Oracle fixture](../scripts/test-oracle-parity.sh) |
| DocumentDB Local pg17-1.0.0 | Native HNSW index attachment, shared reads/writes and filtered cosine search | [DocumentDB fixture](../scripts/test-documentdb-parity.sh) |
| Cosmos NoSQL Linux vNext emulator, pinned digest | Point operations, schema attachment and documented gateway outcomes | [Cosmos fixture](../scripts/test-cosmos-nosql-parity.sh) |

The portable model providers retain their existing [provider contracts](parity.md).
The root Redis, SQLite and Qdrant packages contain core storage APIs alongside
their native surfaces. Keeping those packages together preserves existing
imports and provider behavior.

## Independently versioned experimental modules

Azure AI Search requires a hosted service to verify its native attachment and
ranking. CI runs unit tests and compiles the live fixture; a maintainer can run
the [hosted test](../scripts/test-azure-search-parity.sh) separately. Pinecone
CI exercises a retired local emulator. Production ranking and hosted
availability require independent validation. Its [emulator fixture](../scripts/test-pinecone-parity.sh)
continues to run in CI. Both modules can evolve on their own v0 release lines.

See the [upgrade instructions](upgrading.md) for imports and release tags.
