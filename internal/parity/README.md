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

## Qdrant checkpoint

Run `bash scripts/test-qdrant-parity.sh` with Go, Docker and .NET SDK 10.0.400.
The script creates a disposable Qdrant 1.20.2 container. The fixture covers
collection creation by either client, attaching by physical name, a single
named cosine vector, keyword equality, full unsigned IDs and cross-language
updates and deletes. The two-point fixture exercises the native query endpoint.
Index activation at scale, recall, hybrid search, UUIDs, ambiguous writes,
recovery and the other providers still require evidence.
