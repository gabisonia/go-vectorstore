# PostgreSQL mixed vector workload

Four concurrent workers on a disposable pgvector database completed 7,060–8,230
model operations per second across three five-second runs. Each worker repeated
two point reads, a filtered exact search, and an upsert of one existing record.
Search p95 ranged from 1.09 to 1.46 ms. All three runs returned the expected
records and rankings, canceled one blocked write, and recovered for a subsequent
read. [Download the three measured runs](runs.json).

| Operation | Completed per second, range | p50 latency, range | p95 latency, range |
| --- | ---: | ---: | ---: |
| Point read | 3,530–4,115 | 0.185–0.202 ms | 0.375–0.495 ms |
| Filtered search | 1,765–2,058 | 0.546–0.589 ms | 1.088–1.456 ms |
| Upsert | 1,765–2,058 | 0.808–0.893 ms | 1.500–1.987 ms |

Each range spans the three individual run statistics. Per-operation latency covers
one public API call, including connection acquisition. The elapsed throughput
window includes client-side result checks and draining the last complete cycle.
Workers submit their next operation after completing the previous one. These
figures describe a small, warm dataset at four-way concurrency.

## Common profile for Redis comparison

- Seed 4,096 int64 IDs from 0 through 4,095, each with a 128-dimensional
  float32 vector, a Boolean eligibility field and an int64 revision starting
  at zero. Use Go's `rand.New(rand.NewSource(94))`, drawing `Intn(127)` once
  per coordinate in key order. Generate 16 query vectors from the same stream
  after seeding.
- Filter to keys divisible by four (25% selectivity), rank by exact Euclidean
  distance, and fetch the first eight. PostgreSQL has a primary-key index,
  no secondary filter or vector index, and fresh table statistics. There is
  no HNSW or IVFFlat candidate limit. For tied distances any distinct eligible
  keys at the correct distance are acceptable.
- Worker `w` in cycle `c` reads keys `4*((2*c+w+n) % 1024)` for `n=0,1`,
  searches with query `(c+w) % 16`, then increments the revision of key
  `4*w+1` and upserts its original vector. Reads and search reference rows
  stay stable while writes exercise the same collection. The reference scans
  all eligible vectors before starting the timer. Every result is checked
  outside the API timer. End-of-run reads verify the latest acknowledged
  revision and stored vector for every writer.
- During load, a control transaction locks the last, ineligible row. Once
  PostgreSQL reports the probe upsert is waiting on a lock, cancel its context
  and require `context.Canceled`. Release the lock and read the original
  revision through the probe's pool. This intentional cancellation is counted
  separately from timed operations. An unexpected error fails the run.

## Reproduce

Use Docker and the repository Go toolchain. The integration harness starts and
removes its own database container. First run the 128-row, 16-dimension
correctness profile under the race detector:

```sh
go test -tags integration -race -count=1 -run '^TestIntegrationMixedVectorWorkload$' ./stores/postgres
```

For the measured profile, leave `PGVECTOR_TEST_DSN` unset and run:

```sh
PGVECTOR_TEST_IMAGE=pgvector/pgvector@sha256:484e838076e04939d27d3e176e91c86eea96236126023be8d3059761122e579f \
PGVECTOR_MIXED_MEASURE=1 go test -tags integration -count=3 -v -timeout=3m \
  -run '^TestIntegrationMixedVectorWorkload$' ./stores/postgres
```

Each successful repetition emits a `MIXED_RESULT` JSON object. Require final
package success as well as all three objects. Each repetition uses a fresh
schema on the same PostgreSQL process. Allowing existing OS and database
caches to carry over reflects this setup.

The recorded runs used Go 1.26.1 on an Apple M3 Pro (11 logical CPUs, 36 GiB
host RAM), Docker Desktop 29.9.3 with approximately 7.75 GiB VM RAM, PostgreSQL
16.16 and pgvector 0.8.7 on arm64. Go heap allocations in use immediately
after the load were 8.2–10.0 MiB inclusive of fixture and timing data.
Database container memory after validation was 88.8–104.9 MiB, read from
cgroup v2 `memory.current` including charged cache. These endpoint samples
do not measure peak usage. A supplied disposable DSN omits container memory.
