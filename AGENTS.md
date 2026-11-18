# Working on go-vectorstore

For applications using the library, start with [docs/ai-guide.md](docs/ai-guide.md).
This file describes contributing to this repository.

## Repository map

- `vectordata`: public model/Record contracts, codecs, filters, embeddings and capabilities.
- `stores/memory`, `stores/sqlite`, `stores/postgres`, `stores/redis`: concrete providers.
- `stores/sqlitevec`: optional extension adapter sharing SQLite's engine and format.
- `internal/exactsearch`: shared bounded exact-search ranking for memory/SQLite.
- `internal/qdrantprobe`: opt-in pinned real-server contract probe; no public provider.
- `vectordata/conformance`: shared provider behavior and filter parity fixtures.
- `samples`: runnable workflows; sample-only helpers live in `samples/internal`.
- `samples/sqlite-workload`: opt-in sequential measurement harness; generated JSONL and fixture limits are documented in its README.
- `docs`: usage, provider limits, migrations, architecture and measured performance.

The module requires Go 1.25+. `VERSION` selects the source version being prepared or tagged; CHANGELOG describes
its changes. Verify tag availability in GitHub releases; master can be newer. Keep the original Record API compatible.

## Local verification

Run checks appropriate to the changed paths:

```sh
go test -race -timeout=5m ./...
go vet ./...
bash scripts/test-fuzz.sh 50000x
python3 scripts/check-docs.py
bash scripts/test-samples.sh
bash .github/scripts/release_test.sh
```

These commands need no provider credentials. The sample script creates/removes
its own temporary SQLite files. Documentation checks execute only the explicitly
marked complete AI-guide program; other snippets may be partial recipes.

The SQLite vector design has an isolated, opt-in compatibility probe:

```sh
CGO_ENABLED=0 go test -count=1 -v -tags=sqlitevecprobe -timeout=3m ./internal/sqlitevecprobe
CGO_ENABLED=0 go test -count=1 -tags=sqlitevecprobe -timeout=3m ./stores/sqlitevec
go test -race -count=1 -tags=sqlitevecprobe -run TestNativeVector ./stores/sqlite
```

Its tagged test binary links `modernc.org/sqlite/vec`; the default provider does
not. The probe runs a separate unextended writer and records float32 precision
and trigger compatibility gates. Keep it independent of production capabilities;
see [docs/sqlite-vector-design.md](docs/sqlite-vector-design.md).

The production sqlite-vec adapter uses only the reviewed sequential scalar L1
kernel for native candidates. Verify its version and absence of SIMD build flags
before schema creation, retain the complete top+Skip distance tie group, and
reapply exact Go ranking. Preserve canonical fallback for unsupported shapes and
metrics; vec0 float32 KNN does not have this proof. Keep extension imports out of
the base provider and follow [docs/sqlitevec.md](docs/sqlitevec.md) for limits and
dependency-update review.

The SQLite workload harness has small random/tied correctness profiles in normal
tests and CGo-free platform CI. Full 10k/100k by 128/768 runs are opt-in. Keep
generation/validation outside API timers, require a final JSONL completion event,
and preserve owned-fixture cleanup on cancellation. Reopened handles do not imply
cold OS caches. Record source revision/toolchain/hardware and avoid competing
tests while measuring. See [samples/sqlite-workload](samples/sqlite-workload/README.md).

The Qdrant design has an isolated real-server probe. It starts/removes a pinned
Qdrant 1.20.2 container unless `QDRANT_TEST_URL` supplies a disposable server;
`QDRANT_TEST_API_KEY` is optional. It creates/removes only uniquely owned collections
and aliases. Run `go test -count=1 -race -v -tags=qdrantprobe -timeout=3m ./internal/qdrantprobe`
and `go vet -tags=qdrantprobe ./internal/qdrantprobe`. Default tests need no Qdrant.
See [docs/qdrant-design.md](docs/qdrant-design.md). Preserve canonical envelopes,
portable duplicate/batch/snapshot/filter/distance semantics and explicit unknown
write outcomes. Native `insert_only`, scroll offsets and atomic alias lists do
not by themselves provide those guarantees. Conditional manifest publication is
still a design gate; do not advertise a production connector before proving it.
The [publication experiment](docs/qdrant-publication-proof.md) is tagged test code
with fixed lifetime slots/receipts, logical epoch fencing and no online cleanup.
Preserve positive receipt evidence across superseding writes; absent receipts do
not settle requests still in flight. Never stage an uncertain reservation until
its exact slot descriptor is confirmed. Do not generalize the standalone proof
to replicated topologies, physical reset with active clients or production limits.

PostgreSQL and sample recipe integration tests need Docker:

```sh
go test -count=1 -race -tags=integration -timeout=5m ./stores/postgres
go test -count=1 -race -tags=integration -timeout=5m ./samples/recipes
go test -count=1 -race -tags=integration -timeout=5m ./stores/redis
```

Set `PGVECTOR_TEST_IMAGE=pgvector/pgvector:pg18` for PostgreSQL 18; the default is
pg16. `PGVECTOR_TEST_DSN` can use an existing disposable database instead. Tests
create/drop isolated schemas. Keep the two PostgreSQL package runs in separate
`go test` invocations to avoid concurrent Testcontainers reaper creation in one
shared session. See `.github/workflows/ci.yml` for the full Go and
PostgreSQL matrix and vulnerability scan. Integration runs use -count=1 because test caches
do not represent external database state. Live OpenAI calls are opt-in sample
behavior; normal tests use deterministic embedders and local HTTP stand-ins.

Redis tests start a disposable Redis 8.2 container; `REDIS_TEST_IMAGE=redis:7.4`
selects the other CI version and `REDIS_TEST_ADDR` uses an existing disposable
standalone server. Factories isolate namespaces. Redis snapshots use a shared
canonical codec with SQLite; preserve its format-1 compatibility. Retry only
explicit CAS conflicts, never uncertain network/write replies. Snapshot limits
and full-collection transfer costs must remain documented.

Redis hash storage is opt-in for new collections; existing snapshots require an
explicit upgrade. Stage immutable generations under leases, publish only by the
final manifest SET, and keep reads atomic with manifest verification. Cleanup
must protect current generations and active staging, including uncertain writes.
Keep the format-1 Redis fixture immutable. Hash fields do not imply native index
capabilities; native mode explicitly builds generation-scoped indexes before
publication. Keep native queries in the same Lua execution as manifest/readiness
verification. Preserve NULL truth, integer precision, exact score units and tie
fallback. Unknown publication results must never drop a possibly current index.
RedisJSON format 3 uses immutable JSON wrapper documents with canonical payload
strings; never parse canonical numeric values through RedisJSON or Lua cjson.
Preserve leases, final-SET publication and atomic wrapper reads. Conversion from
snapshots/hashes is explicit; prune only obsolete non-expiring keys with current
manifest protection. JSON native mode advertises scalar/vector/full-text indexes.
Keep canonical payloads opaque, literal tags encoded and integer
NUMERIC components limited to exact 32-bit halves. Preserve version-3 vector-only
schemas/Go filter fallback until an explicit rebuild or native write publishes
version 4 or 5. Full-text hints select version 5 with raw strings under safe
internal TEXT paths; version-4 scalar schemas remain readable until explicit
rebuild/native writes. Validate observable TEXT settings. Native hybrid reads
must fetch both candidate lists and their canonical payloads from one guarded
generation; filter/exclude missing vectors before candidate limits, preserve exact
key ties and RRF score units, and reject missing TEXT indexes without partial
results or implicit rebuilds. KeywordTerms lists OR plain-text AND groups with
bound operands and the same guarded reads. Freeze
operands before embedding callbacks and embed once across read retries.
Keep filtering/KNN/wrapper reads in the
manifest/readiness Lua guard.
Scope index pruning to each layout's lease and namespace.
CI's Redis 8.2 job requires real JSON contracts, native conformance/recall and the
projection proof; Redis 7.4 covers missing-module behavior. CI separates storage
and native index suites to bound their runtime; the expanded native matrix has a
ten-minute timeout, while storage checks retain five minutes.

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
- Keep SQLite format compatibility and consistent WAL snapshots. Opt-in scalar
  projections must retain canonical payloads/registrations and SQL NULL truth,
  maintain every writer through transactional triggers, and backfill existing
  collections only explicitly. Keep candidate predicates conservative under OR/NOT
  and verify real value-index/key-lookup query plans. Retry only
  transaction acquisition/side-effect-free reads; never replay write statements
  or commits after an uncertain result.
- Optional SQLite dense vector projections retain exact Go ranking. Canonical
  triggers must use ordinary SQL to invalidate readiness for unaware writers;
  only aware writes from an already-ready state may maintain and republish it.
  Keep rebuild explicit, keyset-batched and atomic. Rank packed vectors and fetch
  the selected payload page in one deferred WAL snapshot; filtered/binary/sparse
  searches retain canonical fallback. Do not infer native vector index support.
  Require the existing collection/key index for per-record projection cleanup;
  SQLite can otherwise select a collection-only primary-key scan without ANALYZE.
- PostgreSQL migrations separate read-only planning from explicit application.
  Preserve review/staleness validation and additive-application limits.
- ProviderAnnotations are immutable JSON values. Omit empty values and preserve
  exact numbers and stable JSONB round trips. Providers consuming annotations must
  validate owned options before I/O, retain foreign hints and include physical
  options in schema/readiness checks.
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
models through Aspire and can download several GB. Normal tests use local
HTTP stand-ins. Keep framework dependencies out of the root library go.mod.

The Go 1.26+ AppHosts in `samples/aspire-services` and `samples/aspire-vector-search`
also have isolated modules. Run `bash scripts/verify-aspire.sh` with Aspire CLI 17
to restore generated SDKs, build/vet the hosts and test the vector API. Generated
`.aspire` directories stay ignored. The shared host replaces all sample service
orchestration; integration tests retain their disposable Testcontainers setup.
