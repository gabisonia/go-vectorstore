# Working on go-vectorstore

For applications using the library, start with [docs/ai-guide.md](docs/ai-guide.md).
This file describes contributing to this repository.

## Repository map

- `vectordata`: public model/Record contracts, codecs, filters, embeddings and capabilities.
- `stores/memory`, `stores/sqlite`, `stores/postgres`: concrete providers.
- `internal/exactsearch`: shared bounded exact-search ranking for memory/SQLite.
- `vectordata/conformance`: shared provider behavior and filter parity fixtures.
- `samples`: runnable workflows; sample-only helpers live in `samples/internal`.
- `docs`: usage, provider limits, migrations, architecture and measured performance.

The module requires Go 1.25+. `VERSION` selects the source version being prepared or tagged; CHANGELOG describes
its changes. Verify tag availability in GitHub releases; master can be newer. Keep the original Record API compatible.

## Local verification

Run checks appropriate to the changed paths:

```sh
go test -race -timeout=5m ./...
go vet ./...
bash scripts/test-fuzz.sh 3s
python3 scripts/check-docs.py
bash scripts/test-samples.sh
bash .github/scripts/release_test.sh
```

These commands need no provider credentials. The sample script creates/removes
its own temporary SQLite files. Documentation checks execute only the explicitly
marked complete AI-guide program; other snippets may be partial recipes.

PostgreSQL and sample recipe integration tests need Docker:

```sh
go test -race -tags=integration -timeout=5m ./stores/postgres ./samples/recipes
```

Set `PGVECTOR_TEST_IMAGE=pgvector/pgvector:pg18` for PostgreSQL 18; the default is
pg16. `PGVECTOR_TEST_DSN` can use an existing disposable database instead. Tests
create/drop isolated schemas. See `.github/workflows/ci.yml` for the full Go and
PostgreSQL matrix and vulnerability scan. Live OpenAI calls are opt-in sample
behavior; normal tests use deterministic embedders and local HTTP stand-ins.

## Invariants and implementation conventions

- Keep public contracts in `vectordata`; provider-specific options belong to the provider.
- Validate records, filters, vectors and options before side effects. Failed batch
  writes must remain atomic, including generated-key allocation.
- Preserve caller ownership of mutable maps/slices and returned records. Memory
  stores immutable canonical values and replaces them on writes; searches borrow
  them through a shallow snapshot, so never mutate stored record internals.
- Preserve native key and JSON numeric precision, SQL NULL versus JSON null,
  optional-vector behavior, distance/score contracts and stable key tie ordering.
- Keep context cancellation discoverable through errors.Is. Close iterators,
  cursors and transactions on early return. Do not invoke caller codecs while
  holding memory locks or SQLite's single connection.
- Keep SQLite format compatibility and consistent WAL snapshots. Retry only
  transaction acquisition/side-effect-free reads; never replay write statements
  or commits after an uncertain result.
- PostgreSQL migrations separate read-only planning from explicit application.
  Preserve review/staleness validation and additive-application limits.
- Update capabilities, conformance coverage, docs and samples when behavior changes.
  Keep focused samples rerunnable, with explicit persistence/cleanup behavior.
- Use gofmt. Add meaningful tests for changed behavior; use executable Example
  tests for public usage. Benchmarks must state fixtures and avoid throughput
  claims beyond measurements. Do not add dependencies for sample convenience.

## Documentation sources

[docs/model-api.md](docs/model-api.md) is the model usage reference;
[docs/parity.md](docs/parity.md) describes provider differences;
[docs/migrations.md](docs/migrations.md) describes migration semantics.
[docs/production.md](docs/production.md), [docs/releases.md](docs/releases.md) and
[SECURITY.md](SECURITY.md) define operational, compatibility and reporting policy.
`llms.txt` is a discovery index, not a second API specification. Keep its version
and links aligned with VERSION, docs and executable examples.

## Isolated local agent demo

`samples/local-doc-agent` has its own Go module (Go 1.26+) pinned to the published
library and Agent Framework versions. Root `go test ./...` does not traverse that
module. Run `go test -race ./...` and `go vet ./...` inside it; its dedicated CI
jobs cover minimum/current Go and dependency scanning. `smoke.sh` runs real local
models through Docker Compose and can download several GB. Normal tests use local
HTTP stand-ins. Keep framework dependencies out of the root library go.mod.
