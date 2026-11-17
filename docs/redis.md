# Redis model provider

`stores/redis` implements model collections on standalone Redis, with exact dense
search and portable filtering in Go. It is available on master after v0.4.1.
The connector uses atomic collection reads and creates no Redis Search,
ANN, data-property or full-text indexes. It is intended for small collections;
every nonempty record operation transfers the entire collection.

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

Binary/sparse vector definitions return `ErrUnsupported`. Index/full-text hints
are accepted for portability but do not build indexes or enable hybrid search.
Unknown search `ProviderOptions` return `ErrUnsupported`. Migration planning and
application are not advertised. Runtime embedders are never persisted or invoked
while reconstructing stored vectors.

## Storage layouts and explicit upgrade

`StoreOptions.StorageMode` chooses the layout for **new** collections:
`StorageSnapshot` (the default) stores the original format-1 envelope;
`StorageHash` creates a format-2 manifest and immutable per-record hashes.
Existing collections keep their layout regardless of the constructor option.
Both formats support the same public model calls and Go exact search.

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
exact decimal text: future Redis NUMERIC filtering must address its double
precision limits explicitly. The layout is internal, not an application write API.

These hashes are a foundation for native indexing, not an indexing feature.
Production code creates no FT indexes and capability metadata remains unchanged.
A real Redis 8.2 test creates/drops a temporary FLAT index and checks filtered KNN
results over the projected fields. Standalone Redis 7.4 does not need Search.

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
results. Query/filter/search evaluation happens
after the connection is released. Get/GetMany validate only requested records;
Query, filtered Count and Search decode/validate the full snapshot. Nil-filter
Count parses the envelope but skips record validation. QueryIter discards the page
on a decode/validation error before yielding any records. ListCollectionNames uses
deduplicated/sorted `SCAN` results; concurrent create/delete is not a snapshot of
the namespace. Neither format promises incremental result decoding.

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

Get, GetMany, Count and writes transfer O(collection size) bytes. Query, filtered
Count and search additionally materialize a memory collection; retained memory and
decoding allocations grow with collection size. The exact selector bounds its
candidates, but the Redis snapshot itself remains materialized. Hash writes stage
a full new generation, so peak Redis storage includes the current and staged data
plus obsolete generations pending cleanup. Reads still transfer all canonical
payloads; this increment does not accelerate public queries. Concurrent writers
can increase transfers through conflicts. No production throughput claim is made.
Reserve the provider's internal key prefix for this library; do not edit values or
apply TTLs to collection keys.

For hash layout, all generation keys and the manifest must survive together.
Use `noeviction`; independent eviction or external edits can leave dangling
references, which fail reads rather than returning partial records. Redis Search
permissions/modules are unnecessary for production storage in this increment;
hash storage additionally uses HSET/HMGET, PEXPIRE, PERSIST, PTTL, UNLINK and scripts.

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
and current Go and runs the sample twice per layout. Tests cover both layouts with
the shared model, embedding
and filter-parity contracts plus concurrent writes, integer sequence limits,
conflicts, uncertain replies, deadlines, corruption, size limits and client reopen.
Hash checks also cover a native publication error after PERSIST, failed/expiring
staging, retirement during reads, orphan pruning, explicit fixture upgrades and
Redis Search projection compatibility. `REDIS_REQUIRE_SEARCH=true` requires the
projection proof instead of permitting a skip on servers without Search.
