# SQLite model provider

`stores/sqlite` provides durable embedded model collections backed by
[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite). It needs no database
server, extension, C compiler, or API key.

The next optional vector execution path is described in the
[SQLite vector design](sqlite-vector-design.md), with a reproducible driver probe.
The [optional sqlite-vec adapter](sqlitevec.md) executes verified scalar L1
candidates and retains exact Go ranking. The base provider keeps its existing
execution, optionally reading packed projections as described below.

```go
store, err := sqlite.Open(ctx, "/var/lib/myapp/vectors.db", sqlite.StoreOptions{})
if err != nil {
    return err
}
defer store.Close()
// store implements vectordata.ModelStore and CapabilityProvider.
collection, err := store.EnsureModelCollection(ctx, "documents", definition,
    vectordata.ModelCollectionOptions{})
```

Use the same CollectionDefinition, struct codecs, typed wrappers, embedding
defaults, filters, and model operations as memory/PostgreSQL. All 20 native data
types, string/int64/uint64/UUID keys, generated int64/UUID keys, multiple named vectors, nullable vectors,
float16 rounding, and dense/binary/sparse exact search are supported. Binary and
sparse search use their optional interfaces. Vector projections default to off.
Dense and sparse `DistanceL2Squared` search reports squared Euclidean distance,
uses `1 / (1 + distance)` scores, and persists its metric in storage format 1.
See [distance and threshold units](model-api.md#squared-euclidean-distance).

Paths are literal filenames, including punctuation; driver DSNs and custom PRAGMAs
are not accepted. Native text values, keys, and model names must be valid UTF-8;
invalid byte sequences are rejected rather than replaced during JSON encoding.
The parent directory must exist. `:memory:` opens a private
transient database for that Store. Collection names are bound parameters. Runtime
embedders are never serialized; configure them again when reopening a store.
`StoreMetadata` reports system `sqlite` and the absolute literal filename (or
`:memory:`). Collection metadata adds the handle name. Discovery works after
close and does not inspect registrations; see [metadata discovery](model-api.md#store-and-collection-metadata).

## Opt-in scalar indexes

Set `sqlite.StoreOptions{NativeIndexes: true}` to honor `IsIndexed` (or the `index`
struct tag) on string, int64, float64 and bool data properties. Native mode
advertises those four `IndexedDataTypes`; other valid data/index hints retain
existing behavior. Discovery reflects configuration, not collection readiness.
This does not add vector indexes or change exact vector search.

New collections build indexes in the same transaction as creation. Opening a
store or calling EnsureExists on an existing collection does not backfill it.
Its queries/counts retain the existing path until explicit backfill:

```go
store, err := sqlite.Open(ctx, "vectors.db", sqlite.StoreOptions{NativeIndexes: true})
if err != nil {
    return err
}
defer store.Close()
// definition must be the unchanged registered model, including IsIndexed hints.
base, err := store.GetModelCollection("documents", definition, vectordata.ModelCollectionOptions{})
if err != nil {
    return err
}
if err := base.(*sqlite.Collection).RebuildScalarIndexes(ctx); err != nil {
    return err
}
```

Rebuild validates stored records, backfills indexed scalar values, and publishes
readiness in one immediate transaction. It uses bounded decoded record memory,
scans canonical payloads, emits one projection row per indexed field, and holds
the writer lock until completion. Cancellation/failure rolls back the rebuild and preserves any
previous ready index. Rebuild requires native mode. Changing registered model
hints is still a schema change; this API does not migrate definitions.

A shared B-tree covers collection, property, typed value and canonical key.
Eligible positive `Eq` (including NULL), `In`, ranges and `Exists` predicates
select candidate keys, followed by the complete canonical SQL predicate and
existing ordering/paging. AND can use indexed conjuncts; OR restricts candidates
only when every branch is indexable. NOT retains the existing SQL path to preserve
NULL truth. Unsupported filter/sort combinations retain their existing fallback.
Sorts may still require temporary space. These indexes accelerate eligible Query/
QueryIter and filtered Count calls; vector searches still scan all payloads.

Format-1 payloads, registrations and generated sequences remain unchanged.
Additional tables/triggers store one projection row per record/indexed field,
including NULL fields. Insert/upsert/delete, projections and sequence changes
commit or roll back together. Triggers maintain indexes for older binaries and
non-native handles too; non-native handles still use their ordinary read paths.
Collection deletion cascades index registration and projection cleanup. Older
collections can coexist with indexed collections in one file. Do not manually
modify the provider tables or disable foreign keys/triggers.

Index metadata and canonical results are read in the same deferred WAL snapshot.
A conflicting index registration returns `ErrSchemaMismatch` until an explicit
rebuild. Indexes add storage and write work; benchmark realistic selectivity and
field counts. See [index verification](performance.md#sqlite-scalar-index-verification)
and the rerunnable [sample](../samples/sqlite-store/README.md).

## Opt-in dense vector projections

Set `sqlite.StoreOptions{VectorProjections: true}` to build little-endian packed float32 BLOBs
for dense properties in new collections. Float16 properties use their canonical
rounded float32 values. This option is independent of `NativeIndexes`, needs no
extension/C compiler, and does not advertise a vector index or ANN capability.

An unfiltered dense search scans only native keys and the selected packed vector,
ranks with the existing float64 Go distance calculations and bounded top-K heap,
then retrieves/validates the selected canonical payloads in batches of at most
500 keys. Readiness, ranking and every payload batch share one deferred WAL
snapshot. Distances, scores, thresholds, Skip, native key ties, IncludeVectors and
caller ownership keep their existing semantics. The returned page includes every
requested data/vector property, not only the ranked property. All SQL resources
close before typed codecs or application iteration run.

Searches with any filter, binary/sparse searches, and collections whose projection
is absent/dirty/incompatible use the existing canonical scan. No native distance
rounding, candidate truncation or implicit rebuild is introduced. A ready scan
validates projected vectors and returned payloads; it does not decode off-page
canonical payloads. Direct modification of private projection tables is unsupported.

Existing collections require explicit backfill with the unchanged definition:

```go
base, err := store.GetModelCollection("documents", definition, vectordata.ModelCollectionOptions{})
if err != nil {
    return err
}
if err := base.(*sqlite.Collection).RebuildVectorProjections(ctx); err != nil {
    return err
}
```

The Store must have `VectorProjections` enabled. Rebuild validates canonical
records with embedding callbacks disabled, scans in keyset batches of at most
128 decoded records, and publishes all projections/readiness in one immediate
transaction. It holds the writer lock until completion. Cancellation/errors roll
back the entire rebuild, retaining the preceding projection state. Opening a
file, EnsureExists on an existing collection, reads and ordinary writes never
backfill it automatically. A stale collection definition is still a schema error.

Canonical payloads, registrations and generated-key sequences retain format 1.
Ordinary SQL triggers invalidate readiness on canonical inserts/updates/deletes.
Projection-aware writes maintain affected vectors and restore readiness within
the same transaction only if the collection was ready beforehand. Deletions
cascade projection rows. An older binary or a handle with the option disabled
can still write the file; it leaves readiness dirty, so later projection-enabled
reads fall back until explicit rebuild. Updating one row cannot repair an already
dirty collection. Collection deletion removes projection rows and metadata.

Projections duplicate dense vector storage and add write/rebuild work. Scans
remain O(collection size); retained candidates grow with `top + Skip`. For a small
page they avoid decoding full payloads outside that page, while cumulative vector
decoding allocations still grow with the scanned vectors. See the reproducible
[projection checks and benchmark](performance.md#sqlite-vector-projection-verification)
and [sample](../samples/sqlite-store/README.md).

## Durability and concurrency

Open configures WAL mode, FULL synchronization and foreign keys on every
connection. Lock acquisition and retryable reads wait in Go with a five-second
limit, so caller cancellation/deadlines can stop the wait. File durability follows
SQLite/filesystem semantics; the `:memory:` variant is transient. See SQLite's
[WAL documentation](https://www.sqlite.org/wal.html) and
[synchronous settings](https://www.sqlite.org/pragma.html#pragma_synchronous).
Each Store owns one pooled connection; multiple handles/processes can access the
same local database file. SQLite immediate write transactions serialize batches
and generated-key allocation. Competing writers wait up to five seconds or the
caller's context limit, whichever arrives first. Cancellation/deadlines remain
discoverable with `errors.Is(err, context.Canceled)` or
`errors.Is(err, context.DeadlineExceeded)`. Reaching the five-second
contention limit returns the underlying SQLite busy error. Retries acquire a
transaction or repeat a side-effect-free read; write statements and commits are
not replayed.

Insert, Upsert, Delete, collection creation/deletion, and generated sequences are
transactional. Failed record batches roll back every row and sequence update.
Explicit keys do not advance the generated sequence: a collision fails without
overwriting an existing record. SQL NULL and JSON null are stored distinctly;
JSON numbers and exact decimals retain precision. Native timestamps and arrays
are reconstructed according to the registered schema.

Unsigned keys use exact JSON integer encoding and uint64 decoding, including
MaxUint64; SQLite's signed INTEGER range does not limit them. UUIDs are generated
in the application and inserted in the same transaction as the rest of the batch.
Neither unsigned keys nor UUIDs consume `next_key` integer sequence values.

A collection registration must exactly match the normalized definition; a stale
handle returns ErrSchemaMismatch on reads/writes. Storage format 1 is internal to
this provider. Open creates missing provider tables, while EnsureExists creates or
validates a collection. Neither operation migrates an existing definition.

Close the Store before removing/replacing its file. For backup of a live database,
use SQLite-compatible backup tooling that includes WAL state. A copied main file
alone may omit committed WAL data. Use SQLite on a local filesystem that supports
its locking semantics; see the upstream [WAL restrictions](https://www.sqlite.org/wal.html).

## Query costs and provider limits

Get fetches one record directly. GetMany normalizes/deduplicates native keys and
uses the collection/key primary index in chunks of at most 500 keys. Metadata and
all chunks use one deferred read transaction, so results stay on the same WAL
snapshot while other connections can write. Only matching payloads are decoded
and validated; missing keys are omitted, duplicates collapse, and ordering is
unspecified. The connection is released before results reach the caller.

`ListCollectionNamesIter`, `GetManyIter`, `SearchTextIter`, and `SearchInputIter`
are lazy at construction. They load their snapshot/result page before yielding
and release the connection before invoking application callbacks or typed codecs.
They do not promise bounded lookup-result memory or incremental SQL decoding.
GetManyIter shares the same one-snapshot semantics as GetMany. Cancellation is
checked before loading, between results and at exhaustion; a break terminates
without yielding another error. Enumeration never retries after exposing results.

Count with a nil filter uses SQL COUNT over the collection's record index, with
schema/format validation in the same statement. It does not deserialize payloads
and uses bounded Go memory; counting still scans the matching index entries and
can grow with collection size. These fast paths do not invoke embedders.

On master after v0.4.1, top-level `Eq(Column(keyName), value)` and
`In(Column(keyName), values...)` also use the existing collection/key primary index
for `Query`, `QueryIter` and filtered `Count`. Use the logical key name; a storage
name is not a field alias. Keys are normalized and deduplicated before SQL, with
at most 500 unique keys per statement. Signed/unsigned integer precision, UUID
case normalization, native key ordering and NULL operands follow the model
contract. NULL and invalid UTF-8 string operands cannot match stored keys.

Key queries decode and validate only matching payloads, including stored vectors
even when projections omit them. They apply scalar sorting, key tie-breaking and
paging in Go to this matched set, then release results to application code.
Retained memory follows requested keys and matching records; a large matching set
still uses memory even when the returned page is small. Key-filter counts read
index entries without decoding payloads, including damaged selected payloads.
Both paths validate registration/format even for NULL-only or missing keys and
hold one deferred read snapshot across metadata and every key batch. Cancellation
or an error discards partial results and releases the connection before callbacks
or typed codecs run. See [key-filter verification](performance.md#sqlite-key-filter-verification).

Other `Query`/`QueryIter` calls use native SQL filtering, ordering and `LIMIT`/`OFFSET`
when the complete filter and ordering are supported. This covers key comparisons
and string, int64, float64 and bool data columns with `Eq`, `In`, `Gt`, `Gte`, `Lt`,
`Lte`, `Exists`, `And`, `Or` and `Not`. A nil filter also supports native paging.
Use logical property names. Names and values are bound parameters; text uses binary
ordering, NULLs sort last in either direction, and native keys break ties.
Unsigned keys retain their full range. Float64 fields and `In` operands explicitly
use REAL conversion to match typed Go decoding even when their JSON spelling is
an integer. See SQLite's [JSON scalar decoding](https://www.sqlite.org/json1.html#jex).

Native scalar queries decode and validate only the returned page, including its
stored vectors before projection. An off-page invalid vector does not fail that
page. Scalar counts evaluate the relevant JSON fields without decoding vectors
or validating every model field; malformed JSON can still fail SQL evaluation.
Metadata and results share one deferred WAL read snapshot, including empty pages.
Errors discard the whole page, and the connection is released before application
codecs run. Without an available opt-in scalar index, SQLite scans the collection's
payload JSON and may use temporary sorting space. Go result memory
follows the page, but SQL work and sorting costs can grow with collection size.
No schema migration or storage format change is needed. See
[scalar query verification](performance.md#sqlite-scalar-query-verification).

The default exact search streams and validates every payload through one deferred read
transaction, retaining at most `top + Skip` candidates in a heap. Only the returned
page is copied for projection. Ranking uses distance, then native key for ties;
filters, thresholds and missing-vector exclusion apply before paging. Search still
scans and decodes the entire collection: cumulative decoding allocations grow with
collection size, while retained candidate records grow with `top + Skip`.
Large offsets therefore increase memory use. WAL permits other connections to
write during the scan; the Store's own connection remains occupied until it ends.
Filter operands are normalized before acquiring the connection. Ready optional
dense projections avoid full payload decoding for unfiltered searches as described
above; filtered/binary/sparse searches retain the default scan.

Queries/counts with unsupported filter or sort operations load the whole collection
into the memory engine, using O(collection size) memory and scan work. This includes
JSON paths, arrays, decimals, bytes and calendar/timestamp data. Deep/large filter
trees also fall back as a whole to avoid SQLite expression limits. Runtime embedders
are disabled when reading stored records. QueryIter materializes its key matches,
native page or fallback snapshot and SearchIter its result page before
yielding, so application code holds no SQL cursor. Benchmark
realistic sizes before selecting the provider for large collections or concurrent
query services. See the [read benchmarks](performance.md#sqlite-batch-lookup-and-count-verification)
and [exact-search benchmarks](performance.md#exact-search-selection-and-copying).

Without `NativeIndexes`, index flags are portability hints. Opt-in scalar
indexes are described below. Vector indexes, ANN acceleration, full-text search,
hybrid search, PostgreSQL tuning and migration planning/application are not advertised. Unknown ProviderOptions return
ErrUnsupported. Use PostgreSQL when those capabilities are needed.

Examples:

```sh
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db
go run ./samples/model-store -sqlite /tmp/model-store.db
```

The first retains records across reopen; the second demonstrates store-wide
embedding defaults and deletes only the records generated by that run. Tests run
the shared conformance suite plus file reopen, batch rollback, concurrent handles,
context cancellation, and iterator cleanup in normal `go test ./...` CI.
