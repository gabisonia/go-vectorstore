# Production operation

The providers support different workloads. PostgreSQL provides server storage and
ANN/full-text/hybrid capabilities; SQLite provides durable local exact scans;
memory is transient. Redis offers bounded snapshots or opt-in native vector,
scalar and TEXT indexes, rebuilding a complete generation on writes; see
[Redis limits](redis.md). Qdrant's [standalone foundation](qdrant.md) provides
bounded canonical snapshots and exact Go search, with finite retained receipts
and no native indexes or distributed support. Use [capabilities](parity.md) and deployment-specific tests to
choose. The verification below establishes specific failure/compatibility
behavior, not a universal latency, recall or durability guarantee.

## PostgreSQL connections and permissions

Create one application-owned pgx pool per database/configuration and reuse it.
Require TLS certificate/hostname verification for remote databases, with a trusted
CA and `sslmode=verify-full`. The repository's `sslmode=disable` local Aspire DSNs are
local development examples. Keep credentials in your application's secret system;
avoid logging connection strings, query arguments, embeddings or document bodies.

Example pool settings inside application setup (imports: context, time, pgxpool):

```go
cfg, err := pgxpool.ParseConfig(dsn) // dsn supplies verify-full and a trusted CA
if err != nil {
    return err
}
cfg.MaxConns = 10 // Illustrative; size against the server budget and replica count.
cfg.ConnConfig.ConnectTimeout = 5 * time.Second
cfg.MaxConnLifetime = time.Hour
cfg.MaxConnLifetimeJitter = 5 * time.Minute
cfg.MaxConnIdleTime = 10 * time.Minute
cfg.HealthCheckPeriod = time.Minute
pool, err := pgxpool.NewWithConfig(ctx, cfg)
if err != nil {
    return err
}
// Keep pool for the application lifetime; close after requests/iterators stop.
```

Total pools across replicas, background jobs and administrative clients must fit
the database connection budget. Bound application request concurrency as well as
pool size. Set per-operation context deadlines covering pool acquisition,
embedding and database work; configure server statement/lock timeouts appropriate
to the workload. A context deadline is not a substitute for a server-side timeout
when a client disappears. Separate migration/bootstrap timeout budgets from short
request budgets. See the [pgxpool configuration reference](https://pkg.go.dev/github.com/jackc/pgx/v5@v5.9.2/pgxpool#Config).

Have a deployment/admin role install pgvector, create/validate model collections
and indexes, and apply reviewed migrations. Runtime roles should have only the
data/schema/sequence/registry privileges needed by their actual operations. Set
`EnsureExtension=false` when extensions are provisioned administratively. Obtain
existing handles with GetModelCollection/GetTypedModelCollection rather than
running Ensure on every request; Ensure can require DDL rights. Verify permissions
with the real runtime role. Do not give a request-serving role superuser or
migration ownership solely to simplify startup.

## Contexts, iterators and shutdown

Pass the request context through reads, writes and embeddings. Check every
iterator error. Listing, batch, text/input and hybrid iterators check cancellation
before starting, between values and at exhaustion. PostgreSQL also checks between
buffered query/vector-search rows and at exhaustion; cancellation during the final item is reported
if iteration continues. Breaking the loop deliberately ends iteration and closes
its resources. Consumers should also check ctx.Err before expensive per-item work.

Typed model wrappers also check cancellation around each synchronous codec
callback, stopping later callbacks and rejecting canceled encoding before provider
writes or embeddings begin. They cannot interrupt an application callback already
executing. Concurrent codec failures and cancellation preserve both error causes;
decoded partial values are discarded. Cancellation after a provider confirms a
write does not turn that known success into a failed write or trigger a replay.

Release cursors by finishing/breaking the range before issuing another operation
through a single-connection PostgreSQL pool. Memory/SQLite materialize listing,
lookup/query snapshots or search pages and release locks/SQLite's connection
before application iteration. Re-enumerating a sequence reruns reads and query
embedding; cache results explicitly when replay is needed. Stop new requests, finish/cancel
in-flight operations and iterators, then close the owning pool/SQLite Store.

## Failure outcomes and retries

An operation error does not always establish the commit outcome: a connection can
be lost after the database commits but before the caller receives confirmation.
Inspect errors with errors.Is/errors.As; OperationError preserves provider context
and the native cause. Reconcile uncertain outcomes by stable record keys or an
application idempotency protocol before retrying writes. Inspect an `OperationError`
with `errors.As` to obtain its configured system/database/namespace metadata in
addition to collection and operation names. This metadata is a local snapshot;
it does not establish the outcome of a failed operation. Blindly repeating Insert
with generated keys can create duplicates. Upsert can overwrite concurrent changes,
so stable keys alone do not supply application-level concurrency control.

Application UUID text and JSON marshaling errors remain discoverable through
normalization and filter validation, including standard JSON marshaler wrappers.
Use the outer `ErrInvalidRecord`/`ErrInvalidFilter` classification for the operation
and inspect the inner cause for application diagnostics. A filter may wrap record
normalization and match both validation sentinels. Marshaling/validation failures
covered here occur before writes or query embedding; they do not indicate a
database commit attempt.

The library does not replay PostgreSQL writes/commits. SQLite retries acquisition
and side-effect-free reads with cancelable waits capped at five seconds; it does
not replay write statements/commits. A read retry must fit the caller's overall
deadline. Applications own retry limits/backoff for external embedders; partial
embedding requests may have incurred cost even when database writes never begin.
A failed embedding call does not roll back requests already sent to that service.

Connection-termination tests block a read or the second statement of a 501-record
write, terminate its PostgreSQL backend, and verify native error propagation,
transaction rollback and subsequent operation through a replacement connection.
They do not simulate every failover mode or the ambiguous-commit window.

## SQLite files, recovery and backups

Use a local filesystem with SQLite-compatible locking. Each Store has one pooled
connection; WAL allows readers and other handles' writes to overlap, while writes
serialize. FULL synchronization and WAL are configured by the provider. Exact
search still decodes/scans the collection; benchmark record size, vectors, paging,
concurrent readers/writers and memory use. Large offsets retain more candidates.
See [SQLite costs](sqlite.md#query-costs-and-provider-limits).

Do not rename, replace or remove an open database or its WAL/SHM sidecars. For live
backups use SQLite-aware tooling built on the online backup API or an appropriate
snapshot method. A plain copy of the main file can omit committed WAL data. For
an offline copy, stop every process/handle accessing the file and verify a clean
checkpoint before copying the database. Keep backups separate from active data;
filesystem snapshots must consistently capture all required files. See
[SQLite backup guidance](https://www.sqlite.org/backup.html) and
[file corruption pitfalls](https://www.sqlite.org/howtocorrupt.html).

Restore while the service is stopped, using a matched backup rather than mixing
an old main file with another WAL. Restore to an isolated path first, run SQLite
integrity checks, open existing registered collections, and verify expected keys,
counts, generated-key continuation, native values and retrieval results. Reattach
runtime embedders using the original model configuration. Exercise this procedure
with actual backup tooling before relying on it operationally. PostgreSQL backup,
point-in-time recovery and failover likewise belong to the database deployment;
validate restoring pgvector, registrations, indexes and application data together.

Redis recovery verification uses owned fixtures with AOF process-crash recovery
and separate point-in-time RDB restores across supported layouts. Its weekly mixed
workload checks atomic batches, query/search reads and active-generation cleanup,
retaining bounded latency/memory observations. See [Redis recovery and workload
limits](redis.md#recovery-and-mixed-workload-verification). Applications must still
exercise their own persistence configuration, backups and recovery objectives.

The SQLite subprocess test kills the writer without cleanup after a committed
transaction and before commit of a spilled multi-row transaction. Reopening checks
integrity, committed data, rollback and generated-key continuity. This establishes
process-crash recovery in the tested environment; it does not simulate power loss,
a lying disk cache, filesystem corruption or disk exhaustion.

## Embeddings, monitoring and workload validation

Persist an application-level identifier for the embedding model/configuration and
use compatible inputs for ingestion and queries. Equal dimensions do not establish
model compatibility; the library does not persist/verify embedding model identity.
Plan re-embedding and cutover for model changes. Keep model/schema changes explicit.

Monitor operation latency/error/cancellation rates, request/batch sizes, embedding
latency/failures, memory/GC, pgx pool acquisition pressure and database lock/index
behavior. Use application wrappers and pgx tracing/stats; the library does not add
a telemetry backend or log document payloads. Avoid record IDs/queries as unbounded
metric labels. Track restore drills, migration results and scheduled scan failures.

Before rollout, measure your real dimensions/NNZ, filters, corpus size, write/read
concurrency and ANN recall. Validate deadline/connection budgets and index plans.
The [documentation retrieval pilot](../samples/docs-retrieval-pilot/README.md)
provides a reproducible v0.5.0 consumer with local embeddings, manually judged
retrieval queries, concurrent search measurements and an offline SQLite restore.
Use it as a starting point; its small documentation corpus is not a substitute
for evaluating your own data, traffic and recovery requirements.
Deploy a pinned published version, canary it, and retain a rollback/restore plan.
For schema changes follow [migration limits](migrations.md), including blocking
DDL and explicit review. See [release compatibility](releases.md) and
[verification commands](performance.md#production-hardening-verification).
