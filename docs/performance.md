# Performance and verification

## Reproducing benchmarks

Encoding and SQL batch construction do not require PostgreSQL:

```sh
go test -run '^$' -bench 'BenchmarkVectorLiteral|BenchmarkBuildWriteBatch' \
  -benchmem -benchtime=100ms -count=3 ./stores/postgres
```

Full ingestion benchmarks start a disposable pgvector database through
Testcontainers, or use `PGVECTOR_TEST_DSN` if supplied:

```sh
go test -tags=integration -run '^$' -bench BenchmarkIngest \
  -benchmem -benchtime=3x -count=3 -timeout=10m ./stores/postgres
```

The ingestion matrix covers Insert and Upsert, 500 and 5,000 records, and 128 and
1,536 dimensions. Record generation and table truncation are outside the timed
region. Each iteration writes to an empty table; the Upsert benchmark measures
the insert path of `ON CONFLICT`, not updating existing rows. No vector index is
present during ingestion. The benchmark verifies the resulting row count.

## Local measurements

Measured on Apple M3 Pro, darwin/arm64, Go 1.26.1, PostgreSQL 16 through Docker,
using the commands above. The table compares the original ingestion code with
incremental batch preparation and the revised vector formatter. Values are
medians of three runs; the encoding benchmarks use a 100 ms sample per run.

| Operation | Original | Revised |
| --- | ---: | ---: |
| Format one 1,536-dimensional vector, bytes/op | 50,432 | 19,072 |
| Format one 1,536-dimensional vector, allocations/op | 3 | 1 |
| Build 500 rows at 1,536 dimensions, bytes/op | 15,070,252 | 9,788,682 |
| Build 500 rows at 1,536 dimensions, time/op | 28.71 ms | 27.12 ms |
| Insert 5,000 rows at 1,536 dimensions, bytes/op | 1,029,209,261 | 964,041,485 |

The batch builder allocates about 35% fewer bytes for this case. End-to-end timing
varied substantially across runs, so these measurements do not establish a
general ingestion throughput improvement. Repeat longer benchmarks against the
target database and workload before choosing deployment settings.

Writes prepare and execute one batch of at most 500 records at a time in a single
transaction. Temporary encoded batches no longer accumulate across the entire
input. This bounds batch preparation memory; it does not bound the caller's input
slice or driver protocol buffers. Later validation and database errors roll back
all earlier batches.

An allocation profile of the revised 5,000-row ingestion case attributed about
86% of total sampled allocation space to pgx parameter encoding and Bind message
buffers. Further work should compare binary vector encoding and COPY against
this baseline, including connection setup, duplicate handling, and transaction
rollback behavior.

## Model API benchmarks

Model mapping and record preparation run without a database:

```sh
go test -run '^$' -bench 'BenchmarkStructCodec|BenchmarkPrepareModelRecords' \
  -benchmem -benchtime=200ms -count=3 ./vectordata
```

Native model writes and searches use the same Testcontainers/`PGVECTOR_TEST_DSN`
setup as the ingestion benchmark:

```sh
go test -tags=integration -run '^$' -bench 'BenchmarkModelWrite|BenchmarkModelSearch' \
  -benchmem -benchtime=200ms -count=3 -timeout=5m ./stores/postgres
```

`BenchmarkModelWrite` covers 100/1,000 rows at 128/1,536 dimensions without indexes.
Insert starts with an empty table; `upsert_existing` updates preloaded records.
Truncation and preloading run outside the timed region; both modes verify the
final row count. Timings include validation, SQL construction, driver encoding,
network traffic and transaction commit. Schema creation is excluded.

`BenchmarkModelSearch` uses 5,000 indexed, 128-dimensional records and requests
20 results. It compares vector search, a category filter, and full-text/vector
fusion, with and without returning vectors. Search benchmarks include result
mapping but exclude schema creation, ingestion, statistics collection and external
embedding calls. Approximate filtering can return fewer than 20 results.

The following baseline was measured on Apple M3 Pro, darwin/arm64, Go 1.27.4,
PostgreSQL 18.7 / pgvector 0.8.7 in Docker. Values are medians of three 200 ms
samples. These are local fixtures, not production latency or recall guarantees;
short samples and the Docker transport introduce timing variation.

| Operation | Time/op | Bytes/op |
| --- | ---: | ---: |
| Encode a typed model, 128 dimensions | 299 ns | 1,360 |
| Decode a typed model, 128 dimensions | 193 ns | 216 |
| Prepare 100 model records, 128 dimensions | 157 µs | 248,055 |
| Prepare 100 model records, 1,536 dimensions | 320 µs | 813,095 |
| Insert 1,000 records, 128 dimensions | 31.8 ms | 15,326,721 |
| Update 1,000 existing records, 128 dimensions | 32.4 ms | 16,030,165 |
| Insert 1,000 records, 1,536 dimensions | 200 ms | 128,202,760 |
| Vector search, vectors excluded | 0.505 ms | 36,719 |
| Vector search, vectors included | 1.621 ms | 129,374 |
| Filtered vector search, vectors excluded | 0.522 ms | 37,072 |
| Hybrid search, vectors excluded | 1.229 ms | 39,508 |

Codec timings measure mapping alone; they do not include storage validation or
JSON serialization during writes. The projection comparison supports keeping
vectors excluded by default. Large-vector writes still allocate substantially
more than their raw payload: binary vector encoding/COPY remain candidates for a
separate measured optimization, as in the original ingestion profile above.

## Index and recall verification

```sh
go test -race -tags=integration -timeout=5m ./...
```

Metadata index tests use 20,000 records and `EXPLAIN` to verify that Eq/In predicates
can use both GIN operator classes. Sequential scans are discouraged within the
test transaction to check index eligibility; this does not guarantee the planner
will choose that index for every production workload.

Approximate-search tests compare filtered HNSW and IVFFlat results with exact
search over a deterministic 2,000-vector dataset. The tests require recall@5 of
at least 0.8 and explicitly verify vector index usage. Local runs achieved 1.0
for both methods. This small dataset validates the query path and tuning behavior;
it does not predict production recall. Tests also check ascending distances and
restore all request-scoped search settings after successful and failed queries.

Iterative scans and their work limits require pgvector 0.8+. Numeric filter input
validation uses PostgreSQL 16's `pg_input_is_valid`, consistent with the project's
PostgreSQL 16+ requirement.

Model query-plan tests analyze a deterministic 5,000-record fixture, then explain
the actual retrieval, vector and hybrid SQL. With the default planner settings,
the fixture uses HNSW for vector candidates, B-tree for selective category filters,
GIN for array membership, and full-text GIN for keyword candidates. These tests
check index names rather than brittle cost estimates or complete plan trees;
production index selection still depends on statistics and selectivity.

Filter parity tests compare Query/Count results between memory and PostgreSQL
using three fixed seeds, 120 generated nested trees per seed, and a set of base
operator cases over 48 records. A subset also compares Search results without an
approximate index. Separate expected-result cases cover JSON number/string/null
distinctions, typed nil values, literal object keys, array indexing and SQL null
behavior under negation. Run these checks explicitly with:

```sh
go test -tags=integration -run 'TestIntegrationModelQueryIndexes|TestIntegrationModelFilterParity|TestIntegrationModelContract/filter_edges' \
  -timeout=3m ./stores/postgres
```
