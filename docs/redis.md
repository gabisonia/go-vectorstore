# Redis model provider

`stores/redis` implements model collections on standalone Redis. The default
uses atomic collection snapshots with exact dense search and portable filtering
in Go. Opt-in `NativeIndexes` adds Redis Search HNSW and scalar filtering over
immutable hash generations. It is available on master after v0.4.1. The default
encoded collection limit is 16 MiB; writes still process the whole collection.

```go
store, err := redis.New(redis.StoreOptions{
    Addr: "localhost:63799", Namespace: "myapp", DB: 0,
})
if err != nil {
    return err
}
defer store.Close()
products, err := vectordata.EnsureTypedModelCollection[string, Product](
    ctx, store, "products", nil, vectordata.ModelCollectionOptions{},
)
```

Import `stores/redis` and `vectordata` from this module. Use the normal typed or
dynamic model APIs. See the complete [Redis sample](../samples/redis-store/README.md).
`New` and `GetModelCollection` perform no I/O. Ensure operations create/validate
storage; reads return errors when Redis is unavailable. Close releases the owned
go-redis client without deleting collections. This constructor supports a single
standalone address, not Redis Cluster, Sentinel or read-replica routing.

## Supported model behavior

- String, int64, uint64 and UUID keys; generated int64 and UUID keys.
- All 20 portable data types, native arrays, JSON paths and exact decimals.
- Multiple dense float32/float16 vectors, optional vectors and vector projection.
- Cosine, L2, squared L2, inner product and L1 exact search, with the shared score,
  threshold, filter, key tie-breaking and paging contracts.
- Text/custom embedding inputs and store/collection/property defaults.
- Lazy read iterators that buffer snapshots/pages and release client connections
  before callbacks and typed codecs run.
- Local capability/identity metadata and structured operation errors.

Binary/sparse vector definitions return `ErrUnsupported`. Without `NativeIndexes`,
index hints are accepted for portability and build no indexes. Full-text and
hybrid search are unsupported in either mode. Unknown search `ProviderOptions`
return `ErrUnsupported`. Migration planning and
application are not advertised. Runtime embedders are never persisted or invoked
while reconstructing stored vectors.

## Storage layouts and explicit upgrade

`StoreOptions.StorageMode` chooses the layout for **new** collections:
`StorageSnapshot` (the default) stores the original format-1 envelope;
`StorageHash` creates a format-2 manifest and immutable per-record hashes.
Existing collections keep their layout regardless of the constructor option.
Both formats support the same public model calls and Go exact search.
`NativeIndexes: true` selects `StorageHash` when `StorageMode` is unspecified;
explicit snapshot mode with native indexing is rejected.

```go
store, err := redis.New(redis.StoreOptions{
    Addr: "localhost:63799", Namespace: "myapp", StorageMode: redis.StorageHash,
})
```

To upgrade an existing collection, obtain its concrete handle using the same
registered model definition, then explicitly call:

```go
base, err := store.GetModelCollection("products", definition, vectordata.ModelCollectionOptions{})
if err != nil {
    return err
}
if err := base.(*redis.Collection).MigrateToHashStorage(ctx); err != nil {
    return err
}
```

The upgrade preserves schema, records and generated sequence, validates records
before staging, and publishes atomically under concurrent writes. Repeating it
after success is a read-only no-op. New clients reopen either layout; older
format-1-only binaries reject format 2. There is no downgrade operation. This is
provider storage conversion, separate from model-schema migration capabilities.
Snapshot compatibility is checked against an immutable fixture from merged
commit `950401c`; SQLite's format is unchanged.

Format-2 hashes expose `__vs_payload` (canonical record JSON), `__vs_token`
(canonical key token) and `__vs_revision` (generation). String/int64/float64/bool
data fields use `d_` plus the base64url logical name. Native NULL omits that field;
other types remain in the canonical payload. Present dense vectors use `v_` plus
the encoded name, with little-endian FLOAT32 bytes; float16 values are already
rounded before projection. Absent vectors have no blob. Int64 projections use
exact decimal text. Native indexing adds separate high/low 32-bit NUMERIC fields
for exact integer ranges. The layout is internal, not an application write API.

Plain `StorageHash` needs no Search module and creates no indexes. Native mode
adds optional index metadata to the same format-2 manifest; earlier hash readers
ignore these fields. A writer without native indexing can publish a plain hash
generation again. Native readers then use Go fallback until a native write or
explicit rebuild. Older hash writers may leave obsolete indexes for pruning.

## Native indexes and search

Native mode requires standalone Redis with Search, tested on Redis 8.2, and
database zero. The constructor rejects nonzero databases; it does not probe the
server. Missing Search fails index creation without publishing the collection.
Ordinary Redis 7.4 continues to support snapshot/hash modes.

```go
store, err := redis.New(redis.StoreOptions{
    Addr: "localhost:63799", Namespace: "native_app", NativeIndexes: true,
})
// On a new definition, before obtaining/ensuring the collection:
definition.Data[0].IsIndexed = true
definition.Vectors[0].Index = &vectordata.VectorIndexOptions{
    Method: vectordata.IndexMethodHNSW,
}
```

Register the same definition when reopening. Enabling the store option does not
silently convert existing storage or change its registered definition. For an
existing format-1 collection, `MigrateToHashStorage` builds native indexes during
conversion. For an existing hash collection with matching index hints, call
`base.(*redis.Collection).RebuildNativeIndexes(ctx)` explicitly. Repeating the
storage conversion on format 2 is a no-op, so it does not rebuild indexes.

Capabilities are local configuration metadata, not a module/permission probe:

- HNSW supports dense float32/float16 models with cosine, L2, squared L2 and inner
  product: eight advertised tuples, up to 4,096 dimensions. Float16 values are
  rounded before FLOAT32 projection. HNSW `M` is at most 512 and
  `EfConstruction` at most 1,000,000. L1, IVFFlat and other unsupported hints keep
  Go exact search.
- Nonzero vector components must have absolute values in `[1e-10, 1e10]` for the
  native path. Any unsafe stored vector omits that vector field from the entire
  generation's index; an unsafe query also uses Go exact search. The canonical
  values remain intact.
- Keys support equality, `In`, existence and int64/uint64 ranges. Indexed string,
  int64, float64 and bool data properties support equality, `In` and existence;
  indexed int64 also supports ranges. Integer ranges retain all 64 bits through
  split fields. Literal TAG values use hex encoding, including punctuation and
  empty strings; float equality preserves signed-zero equivalence.
- `And`, `Or` and `Not` retain SQL NULL/UNKNOWN semantics. Unsupported filters,
  including float/string ordering, JSON, arrays, calendar/decimal values and
  unindexed data, fall back as a whole. Large literals (over 511 bytes after key
  tokenization), `In` lists over 256, deep/large trees and excessive parameters
  also use Go evaluation. Native schemas allow at most 512 physical fields.

Native filtered Query downloads matching canonical records, validates them and
sorts/pages in Go. Native Count returns the index count without decoding record
payloads or validating their vectors. HNSW selects an enlarged candidate set,
then recomputes distances/scores and applies thresholds, stable key ordering and
paging in Go. Candidates expand when a page underfills or a boundary tie remains;
unresolved ties/underfill use the full exact snapshot. ANN recall otherwise remains
approximate. Missing/not-ready indexes fall back; physical schema drift and
indexing failures return errors. Filters outside the native subset use exact search.

```go
opts := vectordata.ModelSearchOptions{
    ProviderOptions: redis.SearchTuning{EFRuntime: 200},
}
// For a full-snapshot exact reference instead:
opts.ProviderOptions = redis.SearchTuning{Exact: true}
```

`EFRuntime` defaults to 128; accepted values are 0 through 10,000. Positive values
require native mode and cannot be combined with `Exact`. Options are passed by
value. Distances, score units and thresholds retain the public model contract.

## Atomicity and concurrency

Each collection has a Redis string at an internal key containing an encoded
namespace and collection name. Format 1 embeds schema, sequence and all records;
format 2 references an immutable hash generation. Keys and data
are decoded by schema, preserving uint64/JSON/decimal precision and native NULL
versus JSON null. Redis Lua never parses these numbers. SQLite shares the canonical
record codec; its existing format-1 bytes remain compatible.

Writes validate and embed the entire input before mutation, stage a new snapshot
in Go, then execute a parameterized compare-and-swap script. The script compares
the old bytes and publishes with one final `SET`. There are no intermediate write
commands requiring rollback in the snapshot layout. Hash storage first claims a
unique generation lease and stages hashes with one-hour TTLs, in pipeline batches
of 128. The publication script checks all staged records, removes TTLs from the
new generation, then performs the final manifest SET. Failures before SET leave
the old logical records/sequence intact, although auxiliary staging keys may remain.
A fresh revision distinguishes a recreated collection
from an older snapshot. See Redis's [script execution semantics](https://redis.io/docs/latest/develop/programmability/eval-intro/).

Only an explicit conflict reply retries, at most 64 attempts with a context-aware
delay of 1–10 ms. Each attempt reloads and checks schema/format. Embedding callbacks
and generated record UUID allocation occur once per operation. Failed validation, duplicate keys,
sequence exhaustion and size-limit failures leave records and sequence unchanged.
Explicit keys do not advance the integer sequence; generated-key collisions fail
even during Upsert. Network/timeout/Redis errors return immediately, and client
automatic retries are disabled. A lost successful reply can mean the entire batch
committed; reconcile state before deciding whether to retry at the application level.

A snapshot-layout read obtains the collection with one GET. Hash reads fetch a
manifest, then verify those exact bytes and read its payloads in one side-effect-free
Lua execution. A changed manifest retries the read, at most 64 attempts; missing or
damaged referenced hashes fail the whole read. Retiring the previous generation
cannot race this atomic read. Concurrent batch writes cannot produce mixed
results. Go query/filter/search evaluation happens
after the connection is released. Get/GetMany validate only requested records;
Default/fallback Query, filtered Count and Search decode/validate the full snapshot.
Default nil-filter Count parses the envelope but skips record validation. QueryIter discards the page
on a decode/validation error before yielding any records. ListCollectionNames uses
deduplicated/sorted `SCAN` results; concurrent create/delete is not a snapshot of
the namespace. Neither format promises incremental result decoding.

Native writes build a separate index for each immutable generation and wait for
the exact document count, zero indexing failures and the expected physical schema
before publication (at most 30 seconds, bounded by caller context). Failure leaves
the old manifest/sequence intact. Native reads verify the manifest, lease and
index readiness and execute FT.SEARCH in one Lua execution. A concurrent change
retries before returning any rows; retiring an old generation cannot mix reads.
FT.SEARCH uses `TIMEOUT 0` to prevent partial timeout replies. Inside Lua it runs
synchronously on Redis's main thread; a client deadline can return while server
work continues. Reserve all internal keys/index names for this library. Native
Count trusts the immutable index rather than detecting arbitrary external payload
edits; external edits/eviction are unsupported.

## Hash staging and cleanup

Known failed/conflicting staging is cleaned up when possible. After acknowledged
publication, previous generations are retired on a best-effort basis. Cleanup
failure never changes the successful logical result. Lost replies or process
failure can leave obsolete non-expiring keys, including a partially persisted
staging generation. Published data never depends on expiring staging keys.

Call `store.PruneHashStorage(ctx, collectionName)` to reclaim obsolete non-expiring
auxiliary keys, even after collection deletion. It checks the current manifest
atomically with deletion, protects the current generation, and skips TTL staging
that may belong to an active writer. It returns Redis keys removed, including
leases, rather than record count. Concurrent changes may leave work for a later
pass. Unpublished keys with TTLs expire within one hour; pruning does not force
their removal. DeleteCollection removes the logical collection first and attempts
the same cleanup. The library runs no background cleanup workers.

Native indexes do not have TTLs. After acknowledged publication their predecessors
are dropped best-effort, without deleting documents. Uncertain publication never
drops the possibly current index. Call `store.PruneNativeIndexes(ctx, name)` in
database zero to reclaim obsolete indexes, including after deletion or failed
staging. It protects current revisions and active staging leases and returns an
index count, separately from hash pruning's key count. Concurrent changes may
leave work for a later pass. Native DeleteCollection attempts both cleanups.

## Costs and deployment settings

The encoded collection limit defaults to 16 MiB, configurable with
`MaxCollectionBytes` up to 256 MiB. Both layouts bound the canonical snapshot size.
Hash publication additionally counts manifest bytes, lease/key names, hash field
names and values against that limit. Projection overhead can therefore make a
format-1 collection too large to upgrade under the same configured limit. This is
an encoded byte budget, not Redis allocator overhead or peak process memory.
Reads/writes exceeding the configured limit return `ErrInvalidRecord`; they never
truncate. A client with a smaller limit may reject a collection another client
created. The size check occurs after downloading a read, so it does not bound the
network or allocation cost of an externally modified oversized Redis value.

Default and fallback reads transfer O(collection size) bytes; Query, filtered
Count and search additionally decode a memory collection. The exact selector
bounds candidates but the snapshot remains materialized. Native reads still
transfer the O(record count) token manifest; eligible Count avoids record payloads,
filtered Query transfers matching payloads, and HNSW transfers candidate payloads.
Unfiltered Query and Get/GetMany retain the whole-snapshot path. See
[local read measurements](performance.md#redis-native-read-measurements).

Every hash write stages a full generation; native mode also rebuilds its entire
index before publishing. Peak Redis storage includes old and staged data/graphs
plus obsolete generations/indexes pending cleanup. Index allocator overhead is
outside the encoded byte budget. Concurrent writers can multiply this work through
conflicts. This design favors read-heavy, bounded collections; no production
throughput or write-scalability claim is made.
Reserve the provider's internal key prefix for this library; do not edit values or
apply TTLs to collection keys.

For hash layout, all generation keys and the manifest must survive together.
Use `noeviction`; independent eviction or external edits can leave dangling
references, which fail reads rather than returning partial records. Plain storage
needs no Search permissions/module. Hash storage uses HSET/HMGET, PEXPIRE, PERSIST,
PTTL, UNLINK and scripts. Native mode additionally needs FT.CREATE, FT.INFO,
FT.SEARCH and FT.DROPINDEX; explicit index pruning needs FT._LIST. The provider does
not change global Search configuration.

Durability, replication and failover depend on Redis configuration. The local
Compose service enables AOF with `appendfsync everysec`, a persistent volume and
`noeviction`; it can still lose recent acknowledged writes on a crash. Configure
and verify backups/durability for the deployment. This provider does not configure
server persistence or provide failover acknowledgments. See Redis
[persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/).

Use `Username`, `Password` and a verified `TLSConfig` as appropriate. TLS settings
are cloned. Metadata reports system `redis`, configured database number and
namespace, excluding credentials and address. Context deadlines bound client
socket operations; cancellation is checked during preparation, conflict waits and
iteration. In-flight I/O without a deadline can take up to the five-second socket
timeout to return. Preserve errors with `errors.Is`/`errors.As`.

## Verification

```sh
go test -race ./stores/redis
go test -race -tags=integration -timeout=5m ./stores/redis
REDIS_TEST_IMAGE=redis:7.4 go test -count=1 -race -tags=integration -timeout=5m ./stores/redis
```

Normal tests need no server. Integration tests start a disposable `redis:8.2`
container by default; `REDIS_TEST_IMAGE` selects another image and `REDIS_TEST_ADDR`
can point to an existing disposable standalone server. Each factory uses its own
namespace and deletes only its collections. CI exercises Redis 7.4/8.2 on minimum
and current Go and runs the sample twice per layout, plus native mode on 8.2. Tests cover both layouts with
the shared model, embedding
and filter-parity contracts plus concurrent writes, integer sequence limits,
conflicts, uncertain replies, deadlines, corruption, size limits and client reopen.
Hash checks also cover a native publication error after PERSIST, failed/expiring
staging, retirement during reads, orphan pruning, explicit fixture upgrades and
Redis Search projection compatibility. Native suites run the shared contracts,
scalar differential cases, HNSW recall, tie fallback, schema drift, readiness
deadlines, concurrent publication, explicit rebuilds and uncertain-reply cleanup.
`REDIS_REQUIRE_SEARCH=true` requires the native tests and projection proof instead
of permitting skips on servers without Search.
