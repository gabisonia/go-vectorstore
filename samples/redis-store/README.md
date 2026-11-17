# Redis model collections

Run from the repository root with Go 1.25+ and a standalone Redis server. This
sample is on master after v0.4.1 and needs no model download or cloud API key.

```sh
docker compose up -d redis
go run ./samples/redis-store -addr localhost:63799
go run ./samples/redis-store -addr localhost:63799
```

Expected output on both runs:

```text
Guides: 2
Nearest: System architecture (distance 0.000)
Records after reconnect: 3
```

The program upserts three stable IDs into `redis_demo` / `documents`, counts a
category filter and searches supplied three-dimensional vectors. It closes and
reopens the client before counting retained records. Reruns reuse the same
collection. Flags select `-addr`, `-db`, `-namespace` and verified `-tls`;
`REDIS_USERNAME`/`REDIS_PASSWORD` provide optional credentials.

Use a dedicated namespace to create format-2 hash storage, or explicitly upgrade
the existing snapshot fixture. Each command prints the same three result lines:

```sh
go run ./samples/redis-store -addr localhost:63799 -namespace redis_hash_demo -storage hash
go run ./samples/redis-store -addr localhost:63799 -namespace redis_hash_demo -storage hash
go run ./samples/redis-store -addr localhost:63799 -upgrade-hash
```

`-storage` affects new collections only. `-upgrade-hash` explicitly converts the
sample collection, preserving records and generated sequence; repeating it is a
no-op. New clients read both layouts; older format-1-only clients reject the
upgraded layout. Plain hash mode exposes fields without creating indexes.

On standalone Redis 8.2 with Search and database zero, use a separate namespace
for native mode. These commands produce the same output:

```sh
go run ./samples/redis-store -addr localhost:63799 -namespace redis_native_demo -native
go run ./samples/redis-store -addr localhost:63799 -namespace redis_native_demo -native
```

`-native` selects hash storage by default and registers an indexed Category and
HNSW vector. Its definition differs from the ordinary sample, so reusing the old
namespace returns a schema mismatch. Existing hash collections with matching
hints can be indexed explicitly with `Collection.RebuildNativeIndexes`.

Use `-index flat` with another namespace for exhaustive native vector search:

```sh
go run ./samples/redis-store -addr localhost:63799 -namespace redis_flat_demo -native -index flat
go run ./samples/redis-store -addr localhost:63799 -namespace redis_flat_demo -native -index flat
```

Both runs print the same three lines above. `-index` defaults to `hnsw`; `flat`
requires `-native`. Each method is part of the registered model definition, so
changing it within an existing namespace returns a schema mismatch. Rebuilds
retain the registered method.

Create a native TEXT index on Title with a separate namespace:

```sh
go run ./samples/redis-store -addr localhost:63799 -namespace redis_text_demo -native -fulltext
go run ./samples/redis-store -addr localhost:63799 -namespace redis_text_demo -native -fulltext
```

`-fulltext` requires `-native`; it sets `IsFullTextIndexed` on Title and can also
be combined with `-index flat` in its own namespace. Both runs retain the same
three output lines. Creation and the reopened native Count validate the TEXT
schema/readiness while ordinary filtered vector search remains available.
This demonstrates indexing, not a public keyword-query API: SearchText still
creates embeddings and hybrid search remains unsupported. TEXT uses English
stemming, default Redis stopwords and weight 1. Existing definitions with the
same full-text hint can gain TEXT via `RebuildNativeIndexes`; adding the hint to
an existing unmarked definition is a separate model-schema change.

On standalone Redis 8.2 with RedisJSON, create format-3 storage in its own namespace:

```sh
go run ./samples/redis-store -addr localhost:63799 -namespace redis_json_demo -storage json
go run ./samples/redis-store -addr localhost:63799 -namespace redis_json_demo -storage json
```

Explicitly convert the existing hash fixture (or the ordinary snapshot fixture)
without changing its registered model:

```sh
go run ./samples/redis-store -addr localhost:63799 -namespace redis_hash_demo -upgrade-json
go run ./samples/redis-store -addr localhost:63799 -namespace redis_hash_demo -upgrade-json
```

All commands print the same three lines. `-storage json` affects new collections;
`-upgrade-json` explicitly converts snapshots/hashes, preserving records and
sequence. Repeating conversion is a read-only no-op before the sample's ordinary
upsert. Choose one conversion flag per run. Format 3 requires an updated library
and RedisJSON; older binaries reject it. Reverse conversion is unsupported.
JSON stores canonical payload strings inside real JSON documents to preserve
numeric/null precision. Plain JSON filtering/search remain in Go.

With Redis Search and database zero, add native vector indexes in separate namespaces:

```sh
go run ./samples/redis-store -addr localhost:63799 -namespace redis_json_hnsw_demo -storage json -native
go run ./samples/redis-store -addr localhost:63799 -namespace redis_json_hnsw_demo -storage json -native
go run ./samples/redis-store -addr localhost:63799 -namespace redis_json_flat_demo -storage json -native -index flat
go run ./samples/redis-store -addr localhost:63799 -namespace redis_json_flat_demo -storage json -native -index flat
```

All four commands print the same three lines. Count and vector Search both push
the Category filter into Redis using JSON scalar projections. JSON full-text
indexes remain deferred; `-fulltext` requires HASH storage. Rebuilds and native writes build a
new index before atomic publication; ordinary JSON readers/writers remain compatible.
Existing version-3 vector-only generations keep Go filtering until an explicit
rebuild or the sample's native upsert adds version-4 scalar indexes.

Records remain in Redis. To clean up only this fixture, call
`store.DeleteCollection(ctx, "documents")` using its configured namespace/database.
`docker compose stop redis` stops the local server; its volume retains data.
Hash deletion cleans up best-effort. Use `store.PruneHashStorage(ctx, "documents")`
to reclaim obsolete non-expiring hashes if cleanup was interrupted; active staging
is protected and expires within one hour.
Native mode also attempts index cleanup. `store.PruneNativeIndexes(ctx, "documents")`
reclaims obsolete indexes without deleting records and protects current/active
generations. Its result counts indexes rather than Redis keys. JSON uses
`store.PruneJSONStorage(ctx, "documents")` for obsolete non-expiring documents/
leases; deletion attempts JSON cleanup too. Current generations and active TTL
staging are protected. After an uncertain hash-to-JSON conversion, hash/index
pruning can reclaim the former layout's auxiliary data.

Default reads transfer whole snapshots and use Go exact search/filters. Native
mode uses Redis scalar filtering and HNSW candidates with Go scoring and exact
fallback. FLAT retrieves all matching vectors for exact Go selection, including
roundoff-sensitive distances and ties. It remains a full scan for unfiltered
search. Each native write rebuilds the entire generation's index. The default
encoded limit is 16 MiB. Persistence follows Redis configuration;
the local AOF `everysec` setting may lose recent writes on a crash. See
[Redis behavior and limits](../../docs/redis.md).
