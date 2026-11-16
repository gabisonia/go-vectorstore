# Choosing a storage provider

## SQLite

SQLite is a durable embedded provider for local vector data. It stores records in
a local database file and needs no database server. It supports exact dense,
float16, binary and sparse vector search, metadata filtering and optional vectors.

SQLite does not provide physical ANN indexes, full-text search or hybrid search.
Accepted schema index flags are portability hints and do not create those
features. Exact search scans all records. It is useful for a small local assistant
or a development prototype with persistence.

## PostgreSQL

PostgreSQL with pgvector supports persistent server storage, physical HNSW and
IVFFlat vector indexes where supported, native property indexes, full-text search
and keyword/vector hybrid search. Hybrid search combines candidates with
reciprocal rank fusion (RRF). Choose PostgreSQL when hybrid search or ANN indexes
are required. It needs a running PostgreSQL server with pgvector installed.

## Memory

The memory provider is transient: data disappears when the process exits. It
supports exact vector search and filters. It is useful for deterministic tests.
It does not provide persistence, physical ANN indexes or hybrid search.
