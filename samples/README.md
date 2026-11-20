# Runnable samples

Run commands from the repository root with Go 1.26+. Start with a credential-free
model sample; then select SQLite or PostgreSQL when persistence is needed.

| Goal | Sample | Requirements |
| --- | --- | --- |
| Typed CRUD, generated keys, automatic embeddings | [model-store](model-store/README.md) | None; optional SQLite/PostgreSQL |
| Exact uint64 keys and generated UUIDs | [native-keys](native-keys/README.md) | None |
| Custom inputs, image/audio bytes and direct string vector fields | [input-embeddings](input-embeddings/README.md) | None; deterministic fixture |
| Typed float16 generator outputs with unchanged dense model storage | [output-embeddings](output-embeddings/README.md) | None; deterministic fixture |
| Generated typed filters, ordering and nullable properties | [typed-fields](typed-fields/README.md) | None; optional SQLite/PostgreSQL; master after v0.4.1 |
| Durable records, optional vectors, array filters | [sqlite-store](sqlite-store/README.md) | Writable SQLite file |
| Redis model snapshots and dense exact search | [redis-store](redis-store/README.md) | Standalone Redis; available on master after v0.4.1 |
| Bounded Qdrant snapshots and exact search | [qdrant-store](qdrant-store/README.md) | Standalone Qdrant 1.20.2; default Aspire services |
| Typed filtered text retrieval | [typed-search](typed-search/README.md) | None in fixture mode; optional OpenAI |
| Typed retrieval and cited context for generation | [typed-rag](typed-rag/README.md) | None in fixture mode; optional OpenAI |
| Named vectors, scalar/key filters, squared distance, iterators, metadata, capabilities, hybrid, migration preview | [recipes](recipes/README.md) | None for portable recipes; PostgreSQL for hybrid/migration |
| Shared databases and orchestrated sample workflows | [aspire-services](aspire-services/README.md) | Aspire 17, Go 1.26+, Docker |
| Local model with agent-selected search/read tools | [local-doc-agent](local-doc-agent/README.md) | Aspire 17, Go 1.26+, Docker; downloads local models; separate module |
| Ingestion, graded retrieval, concurrent search and offline restore pilot | [docs-retrieval-pilot](docs-retrieval-pilot/README.md) | Aspire 17, Go 1.26+, Docker; MiniLM; separate Go 1.25+ v0.5.0 consumer |
| Aspire 17 Go AppHost, pgvector search UI and dashboard telemetry | [aspire-vector-search](aspire-vector-search/README.md) | Aspire 17, Go 1.26+, Docker; isolated modules; fixture embeddings |
| Original Record API semantic search | [semantic-search](semantic-search/README.md) | PostgreSQL and OpenAI key |
| Original Record API RAG story demo | [ragrimosa](ragrimosa/README.md) | PostgreSQL and OpenAI key |

```sh
go run ./samples/typed-search -category architecture
go run ./samples/typed-rag
go run ./samples/recipes -recipe named-vectors
```

Each README describes expected output, flags, stored fixtures, and cleanup.
Deterministic embedders are instructional fixtures, not semantic models. Remote
modes require explicit opt-in and can incur provider charges. The original Record
API examples remain supported; new model applications should start with the typed
samples. The new samples are on master and included in source versions from v0.4.1;
their library calls use APIs available in v0.4.0, except for input-embeddings,
native-keys and the key-filter, metadata, squared-distance and updated iterator/hybrid recipes,
which require master after v0.4.1. Consult GitHub releases for
published tags. The scalar-query recipe also demonstrates the SQLite SQL paging
optimization on master after v0.4.1; its public calls work on v0.4.0.

## Verification

```sh
go test -race ./samples/...
bash scripts/test-samples.sh
go test -race -tags=integration -timeout=3m ./samples/recipes
```

The first two need no credentials or database server. The smoke script uses a
fresh temporary directory, exercises SQLite reopen/reruns, and removes its files.
The integration command needs Docker or `PGVECTOR_TEST_DSN`. CI runs the portable
checks on minimum/current Go and PostgreSQL recipes on PostgreSQL 16/18.

For application integration and common mistakes, see the [AI usage guide](../docs/ai-guide.md).
