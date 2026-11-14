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
