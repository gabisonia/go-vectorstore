# Redis and PostgreSQL mixed vector workload

The default Redis snapshot provider completed **38.6–46.9 operations/s** on
the four-worker workload. PostgreSQL with pgvector completed **7,060–8,230
operations/s** on the same fixture. Redis transfers the whole collection for
point reads and searches, and concurrent writers compete to replace that single
snapshot. PostgreSQL can retrieve and update individual rows through its primary
key. These measurements apply to the two layouts and versions below.

| Operation | Provider | Operations/s | p50 (ms) | p95 (ms) |
| --- | --- | ---: | ---: | ---: |
| Point read | Redis snapshot | 19.3–23.4 | 18.82–21.02 | 29.88–33.05 |
| Point read | PostgreSQL | 3,530–4,115 | 0.185–0.202 | 0.375–0.495 |
| Filtered search | Redis snapshot | 9.6–11.7 | 79.07–82.61 | 91.92–107.79 |
| Filtered search | PostgreSQL | 1,765–2,058 | 0.546–0.589 | 1.088–1.456 |
| Upsert | Redis snapshot | 9.6–11.7 | 144.19–202.68 | 429.69–622.50 |
| Upsert | PostgreSQL | 1,765–2,058 | 0.808–0.893 | 1.500–1.987 |

Ranges span three runs per provider on the same machine. Each worker repeated
two point reads, one filtered top-eight search and one single-row upsert for
five seconds. Throughput includes draining in-flight cycles and client-side
checks. Latency measures the individual public API calls. The Redis runs took
5.29–5.80 seconds including drain. With this short window, tail estimates
depend on a small number of operations. See the [Redis run data](runs.json),
[PostgreSQL run data](../postgres-mixed/runs.json) and
[fixture and timing definitions](../postgres-mixed/report.md).

## What the two deployments measured

| | Redis 8.2.11 | PostgreSQL 16.16 with pgvector 0.8.7 |
| --- | --- | --- |
| Topology | Standalone container, one canonical snapshot key | Standalone container, one table per collection |
| Durability | AOF enabled, fsync always, scheduled RDB snapshots disabled, no eviction | Standard image WAL and commit settings. The baseline did not record individual server settings. |
| Search | Decode snapshot, filter and rank exact L2 in Go. Native Redis Search indexes disabled. | Apply filter and rank exact L2 on the server. Primary-key index only, fresh table statistics. |
| End-of-load Go heap | 15.8–24.0 MiB | 8.2–10.0 MiB |
| Post-validation container memory | 101.5–121.2 MiB | 88.8–104.9 MiB |
| Unexpected errors / intentional cancellations | 0 / 1 per run | 0 / 1 per run |

Go heap includes the fixture and timing samples. Server memory comes from
cgroup v2 `memory.current` and includes charged cache. Both are endpoint
samples. The runs cover a warm, single-node deployment. Redis hash and JSON
layouts, native vector indexes, replicas and failover are outside this profile.

Both providers checked returned point records, filter eligibility, unique keys,
rank distances against a seeded reference scan, and final written revisions
and vectors. During load the Redis probe canceled a public upsert immediately
after its first successful snapshot GET, before publication. The same handle
read the original revision, acknowledged a fresh write and read back its new
revision. PostgreSQL canceled a write waiting on a server-side row lock.
These probe calls are excluded from the operation counts.

## Reproduce the Redis runs

Use Docker and Go 1.26+. The small integration profile uses 128 rows and
16 dimensions. Each test owns its collection namespace.

```sh
go test -race -tags integration -count=1 -run '^TestIntegrationMixedVectorWorkload$' ./stores/redis
```

For the published 4,096-row, 128-dimension profile, leave `REDIS_TEST_ADDR`
unset. The harness starts and removes the pinned server, and uses a fresh
namespace for each repetition on the same server.

```sh
REDIS_TEST_IMAGE=redis@sha256:b0b01167ff47bc3868e56a967895a26be4230891a1f42a78ab61cdd227accc8d \
REDIS_TEST_RECOVERY=true REDIS_MIXED_MEASURE=1 \
go test -count=3 -tags integration -run '^TestIntegrationMixedVectorWorkload$' -v -timeout=5m ./stores/redis
```

Require three `MIXED_RESULT` objects and final package success. The recorded
environment was Go 1.26.1 on an Apple M3 Pro with 11 logical CPUs and 36 GiB
host RAM, running arm64 Linux containers on Docker Desktop engine 29.9.3
with 7.75 GiB VM RAM. The run data includes the harness revision.
