# Microsoft.Extensions.VectorData parity plan

This plan follows the [parity comparison](parity.md). Deliver one focused PR at a
time, validate it locally and in CI, and continue after its merge. Keep the
existing model interfaces and stored formats compatible. Fix confirmed defects
encountered within each increment; record unrelated work separately.

## 1. Provider annotations — merged

Add optional annotations to key, data and vector definitions, matching the purpose
of Microsoft's [ProviderAnnotations](https://learn.microsoft.com/en-us/dotnet/api/microsoft.extensions.vectordata.vectorstoreproperty.providerannotations?view=net-10.0-pp).
Use JSON values rather than runtime objects because Go definitions are persisted.

- Preserve exact JSON numbers; validate values and copy mutable buffers/maps.
- Omit empty annotations so existing registrations remain byte-compatible.
- Provide typed set/get helpers and provider-owned typed configuration helpers.
- Implement Redis TEXT NoStem as the first concrete option, on HASH and JSON.
  Validate it before I/O and include it in physical-schema drift detection.
- Cover codec/definition ownership, persisted round trips, old-format fixtures,
  ignored foreign-provider hints, malformed options and real hybrid behavior.
- Add executable examples, rerunnable samples, docs and CI coverage. Finish with
  a reviewable PR and passing CI; do not merge automatically.

[PR #55](https://github.com/gabisonia/go-vectorstore/pull/55) merged after all 13 CI
jobs passed. Local verification passed the root race suite, minimum-Go checks,
full PostgreSQL and Redis 8.2/7.4 suites, eight fuzz campaigns and sample reruns.

Acceptance: unchanged models reopen unchanged files; annotations survive every
provider; Redis NoStem changes keyword matching and mismatched indexes fail
closed. Unknown Redis-owned options are rejected rather than silently ignored.

## 2. SQLite vector execution — initial execution and workload evidence merged

First investigate sqlite-vec integration and its platform/driver requirements.
Keep the current pure-Go, extension-free provider available. Propose a separate
opt-in path/package if native extensions require a different driver or toolchain.
Implement canonical CRUD/projection and exact vector execution before additional
index features; prove numerical/filter parity and measure realistic workloads.
Microsoft's SQLite connector uses sqlite-vec; its lack of full-text/hybrid support
means those features are not prerequisites for this increment.

Acceptance: a documented compatibility/design decision followed by independently
reviewable implementation PRs; no silent file conversion or new default native
dependency. Do not call sqlite-vec exact search ANN acceleration.

[PR #56](https://github.com/gabisonia/go-vectorstore/pull/56) merged after all 17 CI
jobs passed. The [SQLite vector design](sqlite-vector-design.md) selects the already-pinned
driver's pure-Go sqlite-vec package through a future opt-in adapter. Its executable
probe covers extension availability, distances, filter placement and a separate
unextended writer. Float32 rounding and mixed-writer trigger behavior require
explicit correctness gates before native ranking ships. The foundation
in [PR #57](https://github.com/gabisonia/go-vectorstore/pull/57) merged after all 17
CI jobs passed. It adds optional ordinary packed dense projections, explicit keyset-batched rebuild,
transactional invalidation for unaware writers and exact Go ranking with page-only
canonical retrieval. Shared conformance, numerical/full-sort parity, released
fixtures, cancellation and WAL snapshot tests cover the foundation. This does not
add extension ranking or advertise vector index capabilities.

The [opt-in sqlite-vec adapter](sqlitevec.md) merged in
[PR #58](https://github.com/gabisonia/go-vectorstore/pull/58) after all 17 CI jobs
passed, including the shared Windows drive-path URI correction. It uses the reviewed scalar L1 double
kernel to select complete distance-boundary candidates, then retains exact Go
ranking and page retrieval in the same WAL snapshot. Version/build flags are
verified before schema creation. Other metrics, filters and dirty projections
retain their existing paths. Conformance, bit-level numerical checks, boundary
ties, native-key precision, concurrency, cancellation and three-OS CGo-free CI
cover this increment. A reproducible 1k/10k L1 benchmark compares all three paths.
The [workload harness](../samples/sqlite-workload/README.md) adds independent
page verification, random/tied profiles, 1%/10% eligibility, top 10/100, warm/reopened
queries, durable batch writes, explicit rebuild and storage observations. Full
10k/100k by 128/768 measurements run outside CI; small correctness/cleanup profiles
run on all three OSes. The [published evidence](benchmarks/sqlite-workload/README.md)
contains complete random/tied runs and all 360 timed page checks, raw JSONL,
checksums and reproducible summaries. Measurement found and corrected a collection
scan per projected record write by requiring the existing key index; regression
tests check actual plans without timing thresholds. The interrupted diagnostic is
labeled separately and excluded from final matrix results. Reopened samples do
not claim cold-disk behavior, and outliers/tied/filtered limits are retained.
Native L2/cosine remain gated by separate numerical proofs; extension availability
alone must not expand native metric claims. The workload increment in
[PR #59](https://github.com/gabisonia/go-vectorstore/pull/59) merged after all 17 CI
jobs passed. Its existing-index cleanup fix, full raw evidence and reproducible
summaries are on master. Controlled cold I/O, concurrency measurements and other
native metrics remain future optimizations rather than prerequisites for Qdrant.

## 3. Connector breadth: Qdrant — compatibility/publication proofs merged; bounded foundation implemented

Add one connector at a time, starting with Qdrant. Begin with collection lifecycle,
typed/dynamic CRUD, dense vectors, filtering, metadata and shared conformance on a
real disposable server. Advertise only verified capabilities. Add sparse/hybrid
support in subsequent PRs as the backend contract permits. Reuse existing codecs
and embedding resolution instead of creating a parallel API.

The [Qdrant design and real-server probe](qdrant-design.md) establish the first
increment on pinned Qdrant 1.20.2. Native IDs, metadata, canonical envelopes and
named-vector/filter primitives work. Duplicate rejection, multi-page snapshots,
missing-value negation and native score precision need explicit adaptation.
`insert_only` silently skips duplicates; atomic aliases do not compare the prior
target. Conditional updates are a candidate for a revision-guarded manifest, but
matching/skipped writes both return completed. This increment adds no public
provider or advertised capabilities. Linux minimum/current-Go CI verifies the
probe without changing default test/server requirements.

[PR #60](https://github.com/gabisonia/go-vectorstore/pull/60) merged after all 19 CI
jobs passed. The follow-up [publication proof](qdrant-publication-proof.md) adds
fourteen real-server cases for independent-client publication, read-only recovery,
unknown/delayed outcomes, slot ownership, logical epoch fencing, consistent readers
and fail-closed resource limits. It retains all old/uncertain slots and receipts;
online reclamation is unsupported. Its one-node/one-auto-shard/one-replica scope
does not establish distributed correctness or physical reset with active clients.

[PR #61](https://github.com/gabisonia/go-vectorstore/pull/61) merged the publication
proof. The [bounded provider foundation](qdrant.md) uses its guarded-manifest and
retained-receipt primitive with one canonical snapshot point. It adds immutable
record/byte/receipt limits, physical ownership, logical tombstones, typed/dynamic
CRUD, exact dense ranking, filters, embeddings, metadata and explicit unknown-write
confirmation. Shared conformance/filter parity, concurrent clients, delayed original
requests and owned-server SIGKILL/restart tests verify this increment. A rerunnable
typed sample uses the shared Aspire services. Native candidate acceleration,
online reclamation, distributed topologies and sparse/hybrid remain later increments;
the foundation is explicitly unsuitable for indefinite unbounded writes.

## 4. Embedding output flexibility — queued

Design an additive contract alongside the existing float32 embedders, informed by
Microsoft's EmbeddingType. Separate input conversion, output representation and
storage precision. Specify conversions and provider support before implementing
the first additional representation; preserve existing explicit binary/sparse
search and float32/float16 behavior. Avoid claiming every .NET connector supports
every output type.

## 5. Typed filter/order references — queued

Add optional generated typed property references over the existing filter AST.
The acceptance test is that invalid property names/types fail compilation in
generated usage, while dynamic string references remain supported. Do not attempt
to translate arbitrary Go callbacks into SQL or emulate LINQ syntax.

## 6. Native service discovery — deferred pending a concrete consumer

Metadata discovery is already covered. Prefer small optional interfaces for
access to underlying clients/decorators when a real integration needs them.
Document lifetime/ownership and typed-wrapper forwarding. A generic service
locator is not required solely to reproduce .NET method names.

## Completion checks for each implementation PR

Run relevant race tests, minimum-Go checks, shared provider contracts and real
backend integration tests. Update capability discovery, examples and provider
limits. Check documentation and sample reruns, open a focused PR and fix CI until
green. After merge, update clean local master and verify the merge includes any
CI corrections. Update this plan's status with the delivered PR.
