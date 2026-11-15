# Documentation

`go-vectorstore` provides model collections across PostgreSQL, embedded SQLite,
and memory, plus the original PostgreSQL Record API.

## Using the library

- [AI usage guide](ai-guide.md): version-aware recipes, a complete executable quickstart, and common integration mistakes
- [Discovery index](../llms.txt): concise links for tools consuming repository documentation

- [Model API](model-api.md): schemas, typed/native keys and data, optional named vectors, filters, embeddings, search and iterators
- [SQLite provider](sqlite.md): setup, persistence, concurrency and snapshot-scan limits
- [Provider and .NET parity](parity.md): implemented workflows, provider capabilities and deliberate differences
- [Upgrading to v0.4.0](upgrading.md): installation, compatibility changes and adoption steps
- [Model migrations](migrations.md): PostgreSQL planning, explicit additive application, drift, backfills and registry finalization
- [Changelog](../CHANGELOG.md): published version history
- [Runnable examples](../samples/README.md): memory, SQLite, PostgreSQL and retrieval demos

## Implementation and verification

- [Architecture](architecture.md): contracts, request flows, schema behavior, filters and indexes
- [Store implementation](stores-implementation.md): provider responsibilities, storage boundaries and invariants
- [Connector development](connector-development.md): implementing and verifying a provider
- [Performance and verification](performance.md): test commands, measured allocations and index/recall checks
