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
upgraded layout. Hashes expose indexable fields but the provider creates no index.

Records remain in Redis. To clean up only this fixture, call
`store.DeleteCollection(ctx, "documents")` using its configured namespace/database.
`docker compose stop redis` stops the local server; its volume retains data.
Hash deletion cleans up best-effort. Use `store.PruneHashStorage(ctx, "documents")`
to reclaim obsolete non-expiring hashes if cleanup was interrupted; active staging
is protected and expires within one hour.

The provider transfers whole collection snapshots and runs exact search/filters
in Go. Its default encoded collection limit is 16 MiB. Accepted index hints do
not build Redis Search or ANN indexes. Persistence follows Redis configuration;
the local AOF `everysec` setting may lose recent writes on a crash. See
[Redis behavior and limits](../../docs/redis.md).
