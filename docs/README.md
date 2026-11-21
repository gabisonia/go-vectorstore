# go-vectorstore documentation

Build vector search into a Go application using typed collections, embeddings,
filters and provider-specific native connectors.

## Get started

Install the published library with Go 1.26 or later.

```sh
go get github.com/gabisonia/go-vectorstore@v0.7.0
```

[Run your first SQLite search](https://github.com/gabisonia/go-vectorstore#your-first-sqlite-search) from an empty directory, or [try the in-memory example](ai-guide.md#minimal-complete-program). Then [compare provider capabilities](parity.md) or
[explore the Go API](https://pkg.go.dev/github.com/gabisonia/go-vectorstore/vectordata).

## Using the library

- [Production operation](production.md): connections, permissions, deadlines, retries, backups and workload validation
- [Documentation retrieval pilot](../samples/docs-retrieval-pilot/README.md): published v0.5.0 consumer, local embeddings, quality/load reports and offline restore drill
- [Compatibility and releases](releases.md): patch policy, storage compatibility and release validation
- [v0.5.0 compatibility review](release-0.5.md): candidate API changes, persisted data and release evidence
- [Security policy](../SECURITY.md): private reporting, supported versions and scheduled checks

- [AI usage guide](ai-guide.md): version-aware recipes, a complete executable quickstart, and common integration mistakes
- [Discovery index](../llms.txt): concise links for tools consuming repository documentation

- [Model API](model-api.md): schemas, typed/native keys and data, optional named vectors, filters, embeddings, search and iterators
- [Typed property references](typed-fields.md): optional generation, compile-checked operands and ordering, regeneration and dynamic JSON paths
- [SQLite provider](sqlite.md): setup, persistence, concurrency and snapshot-scan limits
- [Redis provider](redis.md): snapshot, hash and JSON layouts, native indexes and hybrid search
- [Qdrant provider](qdrant.md): bounded model snapshots and native point access for externally managed collections
- [Native connector support](native-support.md): tested operations, backend versions and independently versioned connectors
- [Opt-in sqlite-vec](sqlitevec.md): exact scalar L1 candidates, precision proof and fallback
- [SQLite vector design](sqlite-vector-design.md): opt-in extension choice, executable compatibility probe and implementation gates
- [Parity implementation plan](parity-plan.md): ordered increments, acceptance checks and delivery status
- [Provider and .NET parity](parity.md): implemented workflows, provider capabilities and deliberate differences
- [Upgrade guide](upgrading.md): installation, compatibility changes and adoption steps
- [Model migrations](migrations.md): PostgreSQL planning, explicit additive application, drift, backfills and registry finalization
- [Changelog](../CHANGELOG.md): published version history
- [Runnable examples](../samples/README.md): memory, SQLite, PostgreSQL and retrieval demos

## Implementation and verification

- [Architecture](architecture.md): contracts, request flows, schema behavior, filters and indexes
- [Store implementation](stores-implementation.md): provider responsibilities, storage boundaries and invariants
- [Connector development](connector-development.md): implementing and verifying a provider
- [Qdrant design and probe](qdrant-design.md): real-server experiments behind the bounded snapshot provider
- [Qdrant publication proof](qdrant-publication-proof.md): bounded manifest/generation experiment, read-only recovery, epoch fencing and retained readers
- [Performance and verification](performance.md): test commands, measured allocations and index/recall checks
