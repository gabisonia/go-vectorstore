# Opt-in sqlite-vec execution

`stores/sqlitevec` selects optional extension execution over the existing SQLite
provider. It uses the already-pinned `modernc.org/sqlite/vec` generated Go package;
no C compiler, server, runtime extension download or dependency update is needed.
The base `stores/sqlite` import does not link the extension.

```go
store, err := sqlitevec.Open(ctx, "vectors.db", sqlitevec.StoreOptions{})
if err != nil {
    return err
}
defer store.Close()
// Store/Collection are aliases for the SQLite provider. Use its model/typed APIs.
```

New collections build packed dense projections atomically. Existing or dirty
collections need explicit `Collection.RebuildVectorProjections(ctx)` using their
unchanged definition. Reads and ordinary writes never backfill automatically.
See [projection lifecycle and mixed writers](sqlite.md#opt-in-dense-vector-projections)
and the rerunnable [sample](../samples/sqlitevec-store/README.md).

## Supported execution and fallback

| Query | Execution |
| --- | --- |
| Unfiltered dense L1, ready projections, reviewed 64-bit little-endian targets | Scalar `vec_distance_L1`, complete boundary candidates, exact Go ranking, canonical page retrieval |
| Cosine, L2, L2Squared or inner product | Existing packed Go path when ready |
| Any filter; binary or sparse vectors | Existing canonical path |
| Missing, dirty or incompatible projections | Canonical path until explicit rebuild |
| Other architectures/operating systems | Existing packed Go path when ready |

The native path covers Linux/macOS/Windows on amd64/arm64, which share the reviewed
generated scalar kernel. CI executes Linux/amd64, macOS/arm64 and Windows/amd64
with CGo disabled, including minimum Go 1.26; other combinations are not a separate
CI platform promise. Float16 uses the already-rounded canonical float32 values.
All portable keys, optional/named vectors, storage-name mappings, embeddings and
typed/dynamic operations retain the shared provider behavior.

The constructor verifies sqlite-vec v0.1.9 and an empty `vec_debug()` SIMD build
flag set before creating provider/projection tables. Missing extensions, unknown
versions/build flags or unknown execution modes return `ErrUnsupported`.
Cancellation remains discoverable through `errors.Is`. This fail-closed check
must be reviewed when upgrading the pinned driver; a version string alone does
not establish numerical equivalence for a different build.

Importing this adapter registers the extension process-wide on subsequently
opened modernc connections, as described by the driver's
[vec package](https://pkg.go.dev/modernc.org/sqlite@v1.59.0/vec).
Availability alone does not change ordinary SQLite handles' execution. The adapter
selects `sqlite.VectorExecutionSQLiteVec`, which implies `VectorProjections`;
advanced callers linking the extension themselves may select that same option on
`sqlite.Open`. `VectorExecutionGo` (the zero value) keeps the base provider's
existing behavior. Metadata identifies the shared backend as `sqlite` and no
vector index, ANN, full-text or hybrid capabilities are added.

## Exact-distance candidate proof

This increment uses the pinned **scalar L1** kernel. It does not use vec0 KNN or
float32 L2/cosine results. The reviewed generated Go `_distance_l1_f32` calls a
sequential loop that converts each float32 operand to float64 before subtraction,
takes its absolute value and accumulates in float64 in coordinate order. The SQL
wrapper passes that double to SQLite without a float32 cast. This matches
`internal/exactsearch.DenseDistance` for L1, bit for bit. Swapping the two operands
does not change the absolute difference. Finite float32 inputs, even at maximum
magnitude, cannot overflow a float64 sum at supported vector dimensions.

The corresponding upstream source is
[`l1_f32` and its dispatch](https://github.com/asg017/sqlite-vec/blob/v0.1.9/sqlite-vec.c#L439)
and the [scalar wrapper](https://github.com/asg017/sqlite-vec/blob/v0.1.9/sqlite-vec.c#L1252).
Its optional NEON path reduces doubles in a different order. Reject SIMD build
flags rather than extending the sequential proof to that implementation. vec0
KNN also casts its result into float32, so its boundary is not interchangeable.

Let `k = top + Skip`, and let `c` be the kth smallest scalar distance across
present selected vectors. Native distances equal canonical Go distances here,
so **every record with distance at most c** contains the complete exact top-k,
including its full tie group. If fewer than k vectors exist, retain all of them.
The conservative bound has factor 1; no fixed oversampling factor is used.

The SQL materialized CTE computes each distance once and retains only key/distance
pairs. It finds `c`, then fetches packed vectors with distance at/below it. Go
revalidates these vectors and reapplies exact distances, score/threshold rules,
native-key ordering and Skip. Key tokens are never used to truncate ties in SQL.
Distance and L1 score thresholds select a prefix in distance order, so retaining
the complete global top-k before those thresholds cannot lose an eligible page.
NULL/negative/infinite distance rows also reach validation; malformed projections
fail without silently disappearing or exposing a partial page.

Readiness, scalar candidate selection, exact ranking and every selected canonical
payload batch use one deferred WAL snapshot. SQL resources close before results
reach application iteration or typed codecs. Context cancellation or a query/
decode error discards the whole page. Canonical payloads, registrations, generated
keys and ordinary-SQL invalidation triggers retain format-1 compatibility.

## Cost and verification

This is exact full scanning, not ANN. SQLite computes a scalar distance for every
present selected vector, materializes O(record count) key/distance pairs, and
selects the boundary using temporary storage/sorting. That work can spill to disk.
Only boundary candidates' packed vectors cross into Go ranking. A large tie group
can contain the whole collection. Large Skip grows both the boundary and retained
Go heap. Projections duplicate vector storage and add transactional write/rebuild
work; enabling the extension is not a general throughput recommendation.

```sh
go test -race ./stores/sqlitevec ./internal/exactsearch
CGO_ENABLED=0 go test -count=1 -tags=sqlitevecprobe ./internal/sqlitevecprobe ./stores/sqlitevec
go test -race -count=1 -tags=sqlitevecprobe -run TestNativeVector ./stores/sqlite
go test -run '^$' -bench BenchmarkL1Execution -benchmem -benchtime=100ms -count=3 ./stores/sqlitevec
```

Tests compare native scalar and Go distance bits across signs, subnormals, maximum
finite values and reductions up to 4,097 dimensions. Shared conformance and the
independent full-sort oracle cover all supported metrics/precisions, fallback
filters, thresholds, offsets and ownership. Focused tests verify native-key ties,
complete tie candidates, real SQL use, concurrent WAL writers, malformed off-page
projections, cancellation, older-writer invalidation and missing-extension rejection
in a separate binary. CI runs the adapter and native SQL proofs on all three OSes.

See [measured fixture and limitations](performance.md#sqlite-vec-l1-verification)
and the [larger workload evidence](benchmarks/sqlite-workload/README.md): 10k/100k,
128/768 dimensions, filtered eligibility, tied vectors, writes/rebuilds and storage.
The write-plan correction requires the existing point index without changing the
format. Controlled cold I/O, tail/concurrent-service measurements and additional
native metrics remain separate work in the [parity plan](parity-plan.md).

## Native .NET collections

Use `sqlitevec.AttachNative(ctx, db, schema)` with a caller-owned
`*sql.DB` opened with the `sqlite` driver to attach to an existing physical
collection. Set `NativeSchema.DataTable`, `Key`, `Text`, `Vector` and
`Dimensions` to the storage names and dimensions in the C# model.
`VectorTable` defaults to the connector's `vec_` prefix.

The adapter supports an integer primary key, one text column and one dense
float32 vec0 vector configured with `DistanceFunction.CosineDistance`.
`Search` returns ascending native cosine distances. `Upsert` and `Delete`
change both tables in a single transaction. An unacknowledged commit returns
`ErrNativeOutcomeUnknown`. The caller owns table creation and schema changes.
Run `bash scripts/test-reference-catalog.sh` to verify both creator directions
and transaction rollback in Docker against the locked C# connector.
