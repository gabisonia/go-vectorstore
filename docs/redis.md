# Redis model provider

`stores/redis` implements model collections on standalone Redis, with exact dense
search and portable filtering in Go. It is available on master after v0.4.1.
This first connector uses atomic collection snapshots and creates no Redis Search,
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

## Atomicity and concurrency

Each collection occupies one Redis string at an internal key containing an encoded
namespace and collection name. The value is a format-1 envelope with the normalized
definition, revision, generated-key sequence and canonical records. Keys and data
are decoded by schema, preserving uint64/JSON/decimal precision and native NULL
versus JSON null. Redis Lua never parses these numbers. SQLite shares the canonical
record codec; its existing format-1 bytes remain compatible.

Writes validate and embed the entire input before mutation, stage a new snapshot
in Go, then execute a parameterized compare-and-swap script. The script compares
the old bytes and publishes with one final `SET`. There are no intermediate write
commands requiring rollback. A fresh revision distinguishes a recreated collection
from an older snapshot. See Redis's [script execution semantics](https://redis.io/docs/latest/develop/programmability/eval-intro/).

Only an explicit conflict reply retries, at most 64 attempts with a context-aware
delay of 1–10 ms. Each attempt reloads and checks schema/format. Embedding callbacks
and UUID generation occur once per operation. Failed validation, duplicate keys,
sequence exhaustion and size-limit failures leave records and sequence unchanged.
Explicit keys do not advance the integer sequence; generated-key collisions fail
even during Upsert. Network/timeout/Redis errors return immediately, and client
automatic retries are disabled. A lost successful reply can mean the entire batch
committed; reconcile state before deciding whether to retry at the application level.

A record read obtains schema, sequence and all records with one `GET`, so concurrent
batch writes cannot produce mixed results. Query/filter/search evaluation happens
after the connection is released. Get/GetMany validate only requested records;
Query, filtered Count and Search decode/validate the full snapshot. Nil-filter
Count parses the envelope but skips record validation. QueryIter discards the page
on a decode/validation error before yielding any records. ListCollectionNames uses
deduplicated/sorted `SCAN` results; concurrent create/delete is not a snapshot of
the namespace.

## Costs and deployment settings

The encoded collection limit defaults to 16 MiB, configurable with
`MaxCollectionBytes` up to 256 MiB. It includes schema, records and JSON overhead.
Reads/writes exceeding the configured limit return `ErrInvalidRecord`; they never
truncate. A client with a smaller limit may reject a collection another client
created. The size check occurs after downloading a read, so it does not bound the
network or allocation cost of an externally modified oversized Redis value.

Get, GetMany, Count and writes transfer O(collection size) bytes. Query, filtered
Count and search additionally materialize a memory collection; retained memory and
decoding allocations grow with collection size. The exact selector bounds its
candidates, but the Redis snapshot itself remains materialized. Concurrent writers
can increase transfers through conflicts. No production throughput claim is made.
Reserve the provider's internal key prefix for this library; do not edit values or
apply TTLs to collection keys.

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
and current Go and runs the sample twice. Tests cover the shared model, embedding
and filter-parity contracts plus concurrent writes, integer sequence limits,
conflicts, uncertain replies, deadlines, corruption, size limits and client reopen.
