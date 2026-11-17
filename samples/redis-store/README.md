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

Records remain in Redis. To clean up only this fixture, call
`store.DeleteCollection(ctx, "documents")` using its configured namespace/database.
`docker compose stop redis` stops the local server; its volume retains data.
Hash deletion cleans up best-effort. Use `store.PruneHashStorage(ctx, "documents")`
to reclaim obsolete non-expiring hashes if cleanup was interrupted; active staging
is protected and expires within one hour.
Native mode also attempts index cleanup. `store.PruneNativeIndexes(ctx, "documents")`
reclaims obsolete indexes without deleting records and protects current/active
generations. Its result counts indexes rather than Redis keys.

Default reads transfer whole snapshots and use Go exact search/filters. Native
mode uses Redis scalar filtering and HNSW candidates with Go scoring and exact
fallback. FLAT retrieves all matching vectors for exact Go selection, including
roundoff-sensitive distances and ties. It remains a full scan for unfiltered
search. Each native write rebuilds the entire generation's index. The default
encoded limit is 16 MiB. Persistence follows Redis configuration;
the local AOF `everysec` setting may lose recent writes on a crash. See
[Redis behavior and limits](../../docs/redis.md).
