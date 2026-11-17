# SQLite vector execution design

Status: investigation/probe merged in [PR #56](https://github.com/gabisonia/go-vectorstore/pull/56).
The packed projection foundation merged in [PR #57](https://github.com/gabisonia/go-vectorstore/pull/57).
The scalar L1 adapter in section B is implemented; additional native metrics remain
gated. Capability discovery does not advertise a vector index.
This document records the investigation gate in the
[parity plan](parity-plan.md).

## Decision

Keep `stores/sqlite` and its existing format-1 canonical records as the default.
Use the already-pinned `modernc.org/sqlite v1.59.0` for the optional path, including
its `modernc.org/sqlite/vec` package. No dependency update, replacement driver,
dynamic library download or C compiler is required for this choice.

The `stores/sqlitevec` adapter imports the extension and shares the
existing SQLite engine, codecs, transaction/retry rules and embedding resolution.
Store/Collection are aliases for the existing provider; `database/sql` ownership
stays internal. Extension imports stay out of the base provider. The adapter's
constructor explicitly selects vector execution and verifies the extension before
creating any projection state. The shared options retain the default Go path.

The extension registers process-wide for subsequently opened modernc connections.
Importing the adapter will therefore make its SQL functions available on ordinary
connections in that application too. Availability must not enable projections or
change the base provider's queries. Applications using only `stores/sqlite` must
continue to build without linking the extension.

The pinned driver's [vec package](https://pkg.go.dev/modernc.org/sqlite@v1.59.0/vec)
bundles sqlite-vec v0.1.9 as generated Go. Its MIT source notice is in the driver's
`LICENSE-SQLITE_VEC`; preserve that notice when redistributing those sources.
Keep the driver and its exact libc dependency aligned. A dependency update must
rerun the probe and review changed numerical/platform behavior.

## Alternatives considered

| Route | Toolchain and integration | Decision |
| --- | --- | --- |
| Pinned modernc `sqlite/vec` | Same driver, generated Go, optional import | Selected; verified by the probe |
| sqlite-vec CGO bindings with mattn | C compiler and a different SQLite driver | Unnecessary driver/toolchain change |
| sqlite-vec ncruces bindings | Custom embedded SQLite WASM build and different driver | Useful alternative, but requires revalidating connection, cancellation and transaction behavior |
| Runtime shared extension loading | Per-platform artifact distribution and driver support | Not selected; do not accept arbitrary extension paths |

The upstream [Go bindings](https://github.com/asg017/sqlite-vec-go-bindings/blob/main/README.md)
describe CGO and ncruces choices. Their CGO auto-registration does not extend a
modernc connection. The selected modernc package is a separate integration.

Microsoft's [SQLite connector](https://learn.microsoft.com/en-us/semantic-kernel/concepts/vector-store-connectors/out-of-the-box-connectors/sqlite-connector)
automatically loads sqlite-vec and separates vector virtual tables from data.
It supports cosine, Manhattan and Euclidean distance, scalar indexing, multiple
vectors and storage-name overrides. Full-text and hybrid search are unsupported.
Matching its purpose does not require matching its physical schema or losing
this library's existing binary/sparse/unsigned-key behavior.

## Reproduce the investigation

From the repository root:

```sh
CGO_ENABLED=0 go test -count=1 -v -tags=sqlitevecprobe -timeout=3m ./internal/sqlitevecprobe
go test -race -count=1 -tags=sqlitevecprobe -timeout=3m ./internal/sqlitevecprobe
```

The tag links the extension only into this test binary. The probe checks the
bundled version, three dense metrics, filtering inside KNN, complete tie ordering,
the base provider's dependency graph, and a separate writer whose binary omits
the extension. CI executes the CGo-free probe on Linux amd64, macOS arm64 and
Windows amd64 with minimum Go 1.25, plus current stable Go on Linux. Other targets
in the driver's source are not a tested platform promise from this project.

The local darwin/arm64 probe reports SQLite 3.53.4 and sqlite-vec v0.1.9.
These are compatibility observations, not throughput measurements. The isolated
probe uses scratch tables, not the provider's private stored format.

## Findings that constrain implementation

**Float32 distances can change the selected page.** For query `[0,0]`, the
extension returns L2 distance `1` for both `[1,0]` and `[1,0.0001]`. The current Go
engine returns `1` and `1.0000000049999997`. Native top-k followed by Go reranking
cannot recover a nearer record already discarded at that boundary. A fixed
oversampling factor does not prove correctness either. Thresholds and paging have
the same issue. Approximate equality in a score assertion is insufficient.

Do not silently replace canonical ranking with float32 ranking. The first
projection implementation must retain `exactsearch.DenseDistance` and native-key
tie ordering. Native execution can ship only with a proved conservative candidate
bound and exact reranking, or a separately reviewed, explicitly documented numeric
contract for an opt-in API. Without that proof/decision, fall back to canonical
ranking before truncation. Include large finite inputs, overflow, cosine edge
cases, float16 rounding, squared distance and threshold boundaries in that review.

**Extension-dependent canonical triggers break unextended writers.** A trigger
that calls `vec_f32` and writes vec0 causes the separate unextended process to fail
its canonical insert. The transaction rolls back. Merely retaining a vec0 table
does not prevent that process from writing unrelated ordinary tables. An ordinary
SQL trigger can invalidate readiness without needing the extension.

**Filters and ties must precede truncation.** A closest excluded record must not
consume k. The probe verifies an eligible-filter KNN query and orders a complete
eligible tie group outside a materialized CTE. This does not prove that arbitrary
model filters can be pushed into vec0. The tested version rejects a query with
both `k` and a pushed-down `LIMIT`; materialization separates those operations.
Sorting a truncated tie group is still incorrect if other tied keys were lost.

Upstream documents both [vec0 and ordinary-table distance queries](https://alexgarcia.xyz/sqlite-vec/features/knn.html).
Select the ordinary-table path first for its filtering flexibility; defer vec0
KNN until candidate, precision and tie-boundary correctness are established.
Neither path should be advertised as approximate nearest-neighbor search.

## Implementation sequence and acceptance gates

### A. Canonical vector projections

Implemented as `sqlite.StoreOptions.VectorProjections` and
`Collection.RebuildVectorProjections`; see [provider usage](sqlite.md#opt-in-dense-vector-projections).
The first read path supports unfiltered dense searches; all filtered/binary/sparse
searches retain canonical evaluation. The design constraints below remain the
acceptance criteria for future changes.

Add an opt-in ordinary projection table keyed by collection, property and exact
canonical key token, with a packed float32 BLOB and versioned readiness metadata.
Keep payloads, definitions and generated-key allocation unchanged. Preserve
logical/storage names through mappings, not interpolated identifiers. Missing
optional vectors produce no row. Decode float16 canonical values into their
already-rounded float32 projection; leave binary/sparse searches on their current
paths initially.

New collections may build projections atomically. Existing collections require
an explicit bounded-memory rebuild under the writer lock. Validate canonical
records without calling embedders, and publish readiness in that same transaction.
Cancellation/failure must preserve the previous usable state. Projection version
and definition mismatch must fail closed or use the canonical fallback; never
rebuild during a read.

Canonical table insert/update/delete triggers must use ordinary SQL only and
invalidate the collection's projection readiness. A projection-aware write may
restore readiness only when the old projection was ready and every affected
vector/key was updated in the same transaction. A single write cannot repair an
already-dirty collection. An unextended writer leaves it dirty; subsequent reads
use canonical scanning until explicit rebuild. All handles must observe readiness
and data in one deferred WAL snapshot. Collection deletion removes projections
and readiness transactionally.

Prove typed/dynamic CRUD, generated keys, rollback, all key types, nullable/named
vectors, storage names, reopened files, old-writer invalidation and concurrent
snapshot behavior. Use the ordinary BLOB projection with Go exact ranking to
avoid decoding every full payload, then fetch only the selected canonical rows.
Apply unsupported filters through canonical evaluation before ranking/limit;
fallback to the existing scan whenever that cannot be guaranteed. This is a
useful, independently reviewable foundation without native ranking claims.

### B. Opt-in extension execution

Implemented for unfiltered dense scalar L1 in [stores/sqlitevec](sqlitevec.md).
The pinned non-SIMD scalar loop uses the same sequential float64 operations as Go,
so complete boundary ties give a conservative candidate bound with factor 1.
Version/build-flag verification and exact Go reranking preserve the existing
numeric contract. L2/cosine, filters and other unsupported shapes retain their
current paths. The acceptance criteria below still govern additional metrics.

Add the optional adapter and extension availability checks; reuse the foundation.
Start with ordinary-table scalar distance execution for supported dense metrics,
subject to the numerical gate above. Preserve all existing metrics and data types
through explicit fallback. Do not derive squared Euclidean by squaring an already
rounded L2 result. Compute public score units through the existing score contract.

Filter/key predicates, missing-vector exclusion, thresholds, distance ordering,
native key ties and Skip must all precede the final page limit. For filters that
cannot be lowered safely, use the existing exact path. Run shared conformance and
adversarial differential tests against memory/default SQLite. Advertise only
verified capabilities, with no ANN/full-text/hybrid additions inferred from the
mere presence of an extension. vec0 remains a later optimization.

### C. Measurement and operational documentation

Implemented in the [workload harness and published evidence](benchmarks/sqlite-workload/README.md).
Complete random/tied runs verify 360 timed pages, include write/rebuild/storage
costs and explicitly distinguish reopened handles from cold OS I/O. The observed
per-record collection scan is corrected with the existing point index. Results
retain outliers, raw artifacts and source/toolchain conditions; additional native
metrics still need separate proofs. The criteria below remain the scope for
future workload expansion, including controlled cold I/O and concurrent services.

Compare default canonical scanning, packed projections with Go ranking and any
approved extension path at 10k/100k records, 128/768 dimensions, top 10/100, and
unfiltered/1%/10% eligible workloads. State payload size, machine, versions,
warm/cold conditions, build/rebuild cost, write overhead and file size. Separate
distance work from JSON decoding and page fetches. Keep reproducible fixtures and
correctness checks alongside benchmarks. Publish results before recommending a
path; this investigation makes no speedup claim.

Run released format fixtures and real mixed-process tests before release. Document
import side effects, rebuild/dirty fallback behavior, resource use and shutdown.
Deliver A, B and C as focused PRs, updating the [parity plan](parity-plan.md) after
each merge.
