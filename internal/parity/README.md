# VectorData interoperability

The reference targets Microsoft.Extensions.VectorData.Abstractions 11.0.0.
The following C# package versions define the catalog for the interoperability PR.
Neon shares the PostgreSQL connector. The deprecated volatile store maps to in-memory.

| Provider | Reference package | Version |
| --- | --- | --- |
| Memory | CommunityToolkit.VectorData.InMemory | 1.0.1 |
| PostgreSQL and Neon | CommunityToolkit.VectorData.PgVector | 1.0.2 |
| Qdrant | CommunityToolkit.VectorData.Qdrant | 1.0.1 |
| Redis | CommunityToolkit.VectorData.Redis | 1.0.0 |
| SQLite | CommunityToolkit.VectorData.SqliteVec | 1.0.4-preview |
| Azure AI Search | CommunityToolkit.VectorData.AzureAISearch | 1.0.1 |
| Azure DocumentDB | CommunityToolkit.VectorData.AzureDocumentDB | 1.1.1 |
| Cosmos DB for NoSQL | CommunityToolkit.VectorData.CosmosNoSql | 1.0.0 |
| SQL Server | CommunityToolkit.VectorData.SqlServer | 1.0.1 |
| MongoDB | Microsoft.SemanticKernel.Connectors.MongoDB | 1.74.0-preview |
| Elasticsearch | Elastic.SemanticKernel.Connectors.Elasticsearch | 0.3.1 |
| Couchbase | CouchbaseConnector.SemanticKernel | 0.2.5 |
| Oracle | Oracle.VectorData | 23.26.300 |
| Pinecone | CommunityToolkit.VectorData.Pinecone | 1.0.0-preview.3 |
| Weaviate | CommunityToolkit.VectorData.Weaviate | 1.0.1 |
| Chroma | CommunityToolkit.VectorData.Chroma | 1.1.0-preview |
| Milvus | CommunityToolkit.VectorData.Milvus | 1.0.0-preview |

Run `bash scripts/test-reference-catalog.sh` with Docker to restore the other
16 reference projects in locked mode and exercise the in-memory stores against
the same records. The memory fixture compares filtered cosine ranking, vector
retrieval and deletion. The SQLite fixture opens the same file with both sqlite-vec
implementations. Each creator supplies an integer key and a named cosine-distance
vector. The Go fixture writes the payload and vector in one transaction. C#
checks nearest-neighbor order and deletes from both physical tables. The Go
fixture calls the public native adapter and checks rollback after the second
statement of a two-table delete fails.

Run `bash scripts/test-postgres-parity.sh` for the shared-table fixture on
PostgreSQL 16 and 18 with pgvector. The Go typed model API and the C# connector
can each create the table, retrieve the other client's written record, rank the
same two points by cosine distance and observe deletion. The C#-created table
uses `GetTypedModelCollection` with explicitly mapped column names.

Run `bash scripts/test-redis-parity.sh` for Redis 8.2 hash and JSON storage.
Each client creates the search index, reads the other client's record and
checks native FLAT cosine retrieval. The Go fixture uses go-redis commands
directly. A public adapter for C#-owned Redis keys remains pending.

The MongoDB, Couchbase and Oracle reference projects
pin patched transitive dependencies flagged by NuGet audit. Their live service
behavior still needs validation.

## Qdrant checkpoint

Run `bash scripts/test-qdrant-parity.sh` with Docker. The script builds the Go
race-test binary and the locked .NET reference runner in Linux containers and
runs them against a disposable Qdrant 1.20.2 instance without publishing ports.

Both Go and C# create physical collections. The unsigned-ID fixture exercises
keyword equality, full uint64 IDs, updates, deletion and a lost acknowledgement
after a committed write. The GUID fixture uses two named cosine vectors with
different dimensions. It compares the IDs and scores of a hybrid vector and
full-text query against the C# connector, then uploads a snapshot to a new
collection and repeats the comparison after deleting from the original.
The indexed fixture waits for native indexing on 8,192 vectors and measures
filtered recall@10 over 32 deterministic queries against exhaustive ranking.

These cases cover standalone Qdrant. The full connector catalog, distributed
recovery, and broader query options still require interoperability evidence.
