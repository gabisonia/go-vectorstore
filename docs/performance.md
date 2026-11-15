# Performance and verification

## Native array verification

```sh
go test -race -tags=integration -run 'NativeArray|ModelContract/capabilities/native_arrays' \
  -v -timeout=3m ./stores/postgres
go test -race -run 'NativeArray' ./vectordata
```

Shared tests cover integer, floating-point and boolean array round trips, aliases,
NULL vs empty arrays, whole-array equality/membership, scalar membership and
negation, filtered vector/hybrid search, mutable-buffer ownership and atomic
failure on invalid elements. Typed tests preserve legacy JSON slice mappings and
check overflow while decoding narrower Go fields.

PostgreSQL tests check additive native-array migrations, required backfill/manual
conversion classification, text/binary vector transport with native arrays, and
array type drift. Read checks reject direct-SQL NULL elements, non-finite floats,
multidimensional arrays and custom lower bounds.

The GIN fixture stores 2,000 records with each membership target present in 1% of
rows. It explains the actual generated retrieval SQL with sequential scans disabled
inside a transaction, verifies each typed predicate can use its GIN index, and
checks results under ordinary execution. This proves index eligibility rather
than predicting production planner choices, latency or selectivity.

## L1 distance and index verification

```sh
go test -race -tags=integration -run 'L1|IndexCapability|ModelContract/capabilities' \
  -v -timeout=3m ./stores/postgres
```

Shared tests check known L1 distances and inverse-distance scores, thresholds,
paging, filters, zero vectors, text queries and hybrid RRF semantics for float32
and float16. Capability tests create and validate all fourteen advertised physical
index combinations. Local validation rejects unsupported L1/IVFFlat choices,
including text-search tuning before embedding.

The HNSW fixture stores 1,000 four-dimensional vectors from a fixed seed, compares
indexed top-ten results with exact L1 search, and verifies the actual generated
query uses each precision's index. It requires recall@10 of at least 0.90 with
`ef_search=100`; a local PostgreSQL 18 run achieved 1.00 for both precisions.
It also checks that a stored zero vector is found through the index. This is a
small regression fixture, not a production recall or latency prediction.

Additional PostgreSQL checks cover additive L1 index migrations, manual review of
metric changes, the Record API and missing extension prerequisites without changing
shared extension objects.

## Half-precision vector verification

Run the precision checks against a disposable PostgreSQL instance:

```sh
go test -race -tags=integration -run 'Float16|HalfVector|VectorPrecision|ModelContract/capabilities/vector_precisions' \
  -v -timeout=3m ./stores/postgres
go test -race -run 'Float16|VectorPrecision' ./vectordata
```

Unit checks cover every finite binary16 value, midpoint ties and adjacent float32
values, subnormals, overflow and cosine vectors that round to zero. Integration
checks compare deterministic float32 values directly with PostgreSQL casts, test
mixed-precision text/binary writes and typed reads, and create/search a
3,072-dimension halfvec HNSW index. Shared conformance checks exercise both memory
and PostgreSQL for all supported metrics, embeddings and text/hybrid queries.

The storage/recall fixture contains 1,000 vectors with 32 dimensions and ten query
vectors from fixed seeds. It compares halfvec HNSW/IVFFlat results with exact
float32 search, requires aggregate recall@10 of at least 0.90, and verifies the
generated search SQL uses the halfvec index. HNSW uses `ef_search=100`; IVFFlat
uses ten lists and nine probes. A local PostgreSQL 18 run achieved recall of 1.00
and 0.98 respectively. These small, synthetic checks are regression tests, not a
prediction of production recall or latency.

The same run measured stored vector columns at 136 bytes for float32 and 69 bytes
for float16 (`pg_column_size`); the smaller value benefits from PostgreSQL's short
varlena header. The underlying formats use four versus two bytes per component,
plus headers. This does not measure total row, table or index size. Writes still
use the existing float32 wire encoding followed by an explicit halfvec cast.

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

A profile of the earlier text-based 5,000-row ingestion case attributed about
86% of total sampled allocation space to pgx parameter encoding and Bind message
buffers. The transport comparison below evaluates binary vector encoding and COPY against
text, including connection setup and transaction commit.

## Model API benchmarks

Model mapping and record preparation run without a database:

```sh
go test -run '^$' -bench 'BenchmarkStructCodec|BenchmarkPrepareModelRecords' \
  -benchmem -benchtime=200ms -count=3 ./vectordata
```

Native model writes and searches use the same Testcontainers/`PGVECTOR_TEST_DSN`
setup as the ingestion benchmark:

```sh
go test -tags=integration -run '^$' -bench '^(BenchmarkModelWrite|BenchmarkModelSearch)$' \
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
vectors excluded by default. The preceding table records the original model API
baseline, before binary ingestion was added. The transport comparison below
measures the current write path against text encoding and COPY staging.

## Binary vector ingestion and COPY comparison

PostgreSQL writes now select binary vector parameters when a request contains at
least 1,024 float32 values across its records and named vectors. Smaller requests
retain text parameters to avoid codec/type-lookup overhead. This heuristic follows
the local small-write benchmarks; it is not a latency guarantee for every dataset.
Both the original Record API and model API use this selection without new options.

The encoder appends pgvector's binary payload directly to pgx's parameter buffer.
It follows [pgvector's binary input/output format](https://github.com/pgvector/pgvector/blob/v0.8.0/src/vector.c#L336):
a dimension/reserved header and network-order float32 elements. It checks dimension
representation bounds to prevent truncation; existing public write validation
still checks dimensions and finite values before serialization. Text-only parameter
protocols retain the encoder's text fallback.

A binary write resolves the vector type inside its transaction, then installs a
codec on an operation-scoped copy of the leased connection's type map. The lease
stays owned until the transaction and codec cleanup finish. The original type map,
including caller registrations and wrappers, is restored before returning the
connection to the pool. Pool configuration and AfterConnect hooks need no changes.
SQL write statements retain their existing per-record/model and per-batch/Record
shape, generated-key handling, returned-key ordering and conflict behavior.

Reproduce the transport comparison separately from concurrent tests or workloads:

```sh
go test -tags=integration -run '^$' \
  -bench '^(BenchmarkModelWriteTransport|BenchmarkSmallModelWriteTransport)$' \
  -benchmem -benchtime=200ms -count=3 -timeout=5m ./stores/postgres
go test -tags=integration -run '^$' -bench '^BenchmarkRecordWriteTransport$' \
  -benchmem -benchtime=3x -count=3 -timeout=5m ./stores/postgres
go test -run '^$' -bench '^BenchmarkVectorParameterEncoding$' \
  -benchmem -benchtime=200ms -count=3 ./stores/postgres
```

`text` and `binary` run the same model validation, SQL writes and returned-key
mapping with different vector encoders. `copy_stage` is a benchmark-only prototype
for supplied unique int64 keys: it creates a temporary staging table, performs
binary COPY, then inserts/upserts into the target with RETURNING. All candidates
include validation, transaction setup and commit. Upsert cases preload existing
records outside the timer. Count/key-count checks validate every benchmark case.
COPY comparisons use the same unindexed data fixture as the model write benchmark.

Local medians on Apple M3 Pro, darwin/arm64, Go 1.27.4, PostgreSQL 18.7 / pgvector
0.8.7 in Docker. Model rows use three 200 ms samples; Record rows use three samples
of three writes each. Each operation writes 1,000 records:

| API / operation | Dimensions | Transport | Time/op | Bytes/op |
| --- | ---: | --- | ---: | ---: |
| Model insert | 128 | Text | 27.19 ms | 15,310,123 |
| Model insert | 128 | Binary | 15.29 ms | 7,503,389 |
| Model insert | 128 | COPY staging | 11.09 ms | 2,060,197 |
| Model insert | 1,536 | Text | 182.77 ms | 128,187,192 |
| Model insert | 1,536 | Binary | 62.49 ms | 49,447,584 |
| Model insert | 1,536 | COPY staging | 75.01 ms | 7,649,672 |
| Model update existing | 1,536 | Text | 204.40 ms | 128,870,472 |
| Model update existing | 1,536 | Binary | 71.18 ms | 50,152,098 |
| Model update existing | 1,536 | COPY staging | 102.01 ms | 7,672,368 |
| Record insert | 1,536 | Text | 299.78 ms | 192,791,642 |
| Record insert | 1,536 | Binary | 76.38 ms | 70,555,093 |
| Record update existing | 1,536 | Text | 307.07 ms | 192,789,024 |
| Record update existing | 1,536 | Binary | 83.54 ms | 70,554,965 |

Binary parameters reduce allocated bytes by about 61% for the 1,536-dimensional
model insert fixture and 63% for the equivalent Record fixture. Encoding one
1,536-dimensional parameter into a reusable buffer measured 45.7 µs / 19,072 bytes
for text and 0.543 µs / zero additional bytes for binary. Whole writes still
allocate prepared records, SQL/argument structures, Bind messages and driver
buffers; these encoding-only results do not represent end-to-end allocations.

Small-request measurements support retaining the text path: one 3-dimensional
model update measured 0.705 ms with text versus 1.022 ms with binary setup; one
128-dimensional update measured 0.678 ms versus 0.828 ms. Eight 128-dimensional
records and one 1,536-dimensional record favored binary in this run. Timing is
sensitive to Docker transport, cache state and host load; compare longer runs
against production workloads before making throughput or latency projections.

COPY staging remains a benchmark prototype. It needs temporary-table privileges,
and its set-based INSERT changes statement-trigger behavior and needs additional
design for generated IDs, repeated-key upserts and trigger-modified returned keys.
It therefore is not selected by the public APIs. Binary parameters improve the
large-vector case while preserving the existing write contract and permissions.

To profile the selected public model path without also selecting the transport
comparison benchmark, anchor the benchmark name:

```sh
go test -tags=integration -run '^$' -bench '^BenchmarkModelWrite$' \
  -benchtime=3x -count=3 -memprofile=/tmp/vectorstore-ingest.mem \
  -o /tmp/vectorstore-ingest.test -timeout=5m ./stores/postgres
go tool pprof -alloc_space /tmp/vectorstore-ingest.test /tmp/vectorstore-ingest.mem
```

## Collection lookup benchmarks

Compare targeted lookups with the previous list-and-search strategy:

```sh
go test -tags=integration -run '^$' -bench BenchmarkCollectionExists \
  -benchmem -benchtime=200ms -count=3 -timeout=5m ./stores/postgres
```

Fixtures contain either 1 or 1,000 vector collections plus one registered scalar
model. Catalog population runs outside the timed region. `models`, `scalar` and
`missing` benchmark named lookups; `list_reference` lists the namespace and checks
for the target, matching the previous implementation. Query tracing in integration
tests also verifies that existing vector and missing relations need one query,
scalar models need two, and every query returns at most one row.

Local medians of three 200 ms samples on Apple M3 Pro, darwin/arm64, Go 1.27.4
and PostgreSQL 18 in Docker:

| Lookup | Vector collections | Time/op | Bytes/op |
| --- | ---: | ---: | ---: |
| Named vector model | 1 | 0.146 ms | 795 |
| Named vector model | 1,000 | 0.162 ms | 796 |
| Named scalar model | 1,000 | 0.302 ms | 1,465 |
| Missing relation | 1,000 | 0.153 ms | 700 |
| List reference | 1 | 0.500 ms | 2,235 |
| List reference | 1,000 | 4.084 ms | 84,415 |

The named lookup avoids transferring and allocating every collection name.
These local timings include Docker/network overhead and are workload-specific;
repeat against the target deployment when assessing latency.

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
