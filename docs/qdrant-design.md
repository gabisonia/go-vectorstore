# Qdrant connector design and compatibility gates

The first Qdrant increment established an executable backend assessment. The
[bounded `stores/qdrant` foundation](qdrant.md) now uses its guarded-manifest
primitive for canonical snapshots. The assessment protects the model contract while deciding
how collection lifecycle, typed/dynamic CRUD, dense search, filtering and discovery
can use Qdrant. The probe itself advertises no capabilities; the provider reports
only its verified canonical dense execution.

The [ordered parity plan](parity-plan.md) delivers one reviewable PR at a time.
This follows the merged SQLite workload increment in
[PR #59](https://github.com/gabisonia/go-vectorstore/pull/59).

## Verified backend and scope

The [real-server probe](../internal/qdrantprobe/probe_test.go) targets Qdrant
**1.20.2**, release commit `23065a0bf6857c3e5da45f6c8939cc746cc0580e`.
The 2026-11-18 local run used Docker image `qdrant/qdrant:v1.20.2`, index digest
`sha256:d277592721d2e3b73f9b797b53cf05e6a5bb2134f6d13fdbda4ca51782afbcac`,
darwin/arm64, Go 1.26.1, and one local shard/replica. All eleven tests passed with
race detection. The tests assert observable server behavior, including differences
that an adapter must handle; passing them does not mean a provider is implemented
or that distributed correctness, restart durability or performance is established.

The follow-up [publication protocol proof](qdrant-publication-proof.md) exercises
immutable generations, retained receipts, epoch fencing, uncertain outcomes and
reader retention with explicit standalone topology and finite resource bounds.
Its code is test-only; it does not expose a provider or online reclamation.

CI repeats them on Linux with minimum/current Go. Normal `go test ./...` does not
start Docker or connect to Qdrant. The probe uses the existing Testcontainers
dependency and standard-library HTTP/JSON; it adds no SDK dependency. Revisit the
gates deliberately when changing the pinned release.

Run from the repository root:

```sh
go test -count=1 -race -v -tags=qdrantprobe -timeout=3m ./internal/qdrantprobe
go vet -tags=qdrantprobe ./internal/qdrantprobe
```

By default the suite owns and removes one disposable container. Alternatively set
`QDRANT_TEST_URL=http://localhost:6333` for a disposable Qdrant 1.20.2 server;
`QDRANT_TEST_API_KEY` is optional. Tests create unpredictable
`vectorstore_probe_...` collections and an owned alias, then remove only those
resources, even when a test fails. They do not reset the supplied server. Requests
have context/deadline and bounded-response handling, preserve cancellation, and
never retry writes. Redirects are rejected. This is test infrastructure, not a
public client or a recommended deployment configuration.

## Observations and required adapter behavior

| Contract | Real-server observation | Consequence |
| --- | --- | --- |
| Collection identity | Named dense-vector settings and an opaque definition string in collection metadata round-trip; lifecycle/listing work | A provider must validate both canonical registration and physical configuration, including reopened/mismatched collections |
| Native IDs | UUID and full uint64 IDs round-trip, including 2^53+1 and MaxUint64; arbitrary strings and negative integers are rejected | Preserve logical keys in canonical records; native ID mapping must be explicit, collision checked and independent of float64 |
| Canonical values | A shared `modelcodec` envelope stored as a payload string round-trips byte for byte, including exact JSON numbers and native-null versus JSON-null | Reuse this envelope for authority; do not decode authoritative values through Qdrant numeric payloads |
| Cosine retrieval | Qdrant returns `[0.6,0.8]` after storing `[3,4]` | Native vectors are derived search projections; return canonical vectors to callers |
| Insert | Default upsert replaces an existing ID; `insert_only` returns completed while skipping a duplicate and inserting the other row | Neither operation directly provides the library's duplicate-rejecting, whole-batch Insert contract |
| Conditional update | `update_filter` skips a nonmatching existing point, but still inserts a missing point; `update_only` prevents that insertion | Publication must distinguish initialization from revision-guarded update; a false update filter alone is not a creation guard |
| Publication reply | Competing revision-filtered single-point updates both report completed; readback observes a chosen token and stale revision updates skip it | A reply is not proof that a condition matched; confirmation and unknown-outcome handling are required |
| Native distance | Exact Euclid scores for `[1,0]` and `[1,0.0001]` both equal 1; canonical float64 L2 distances are 1 and 1.0000000049999997 | `params.exact=true` does not establish this library's exact ordering/threshold units; bounded native candidates need an independent recall proof |
| Filtering/optional vectors | Native filters run before the result limit, and points missing the selected named vector do not match vector queries | Useful primitives, but not proof of every portable AST or type combination |
| Missing-path negation | Native `must_not` includes a missing path; portable NOT over that comparison preserves unknown and excludes it | Do not mechanically translate NOT into `must_not`; first use canonical Go evaluation or proven conservative candidate predicates |
| Scroll consistency | A continuation reads old ID1 and new ID2 across an acknowledged write | A scroll offset does not pin a snapshot; mutable points cannot supply consistent multi-page enumeration/ranking |
| Aliases | Atomic delete/create actions permit a stale writer to replace a newer alias target | Alias action atomicity is not revision compare-and-swap; it alone cannot protect concurrent publication |

The executable tests retain the concrete numerical and missing-value fixtures.
They do not use timing thresholds or assert a particular native tie ordering.
Native Euclid is a distance, not a squared-distance guarantee. Additional metrics,
precisions, ANN and hybrid/sparse search require their own mapping and tests.

## Storage decision

A direct mutable point-per-record adapter is deferred: duplicate prechecks can
race, a successful `insert_only` can partially accept a logical batch, and a scan
can combine states. A process-local mutex would not protect another client.
Atomic alias replacement also permits lost updates. Do not weaken the portable
Insert, schema, snapshot or exact-distance contracts to accommodate those paths.

The [publication experiment](qdrant-publication-proof.md) implements a bounded
**immutable generation plus revision-guarded manifest** protocol for the topology
it checks. The first provider deliberately keeps its entire bounded canonical
dataset in the manifest point. This avoids multi-point staging/reader retention
for exact execution while preserving guarded publication, logical epochs and
retained positive receipts. Its format, limits and restart evidence are documented
[separately](qdrant.md); it does not copy the experiment's slot layout.

The following requirements govern future native multi-point execution:

1. Keep the definition, generated-key state and canonical records authoritative.
   Prepare/validate a whole logical mutation using existing model preparation and
   codecs before publication. Embedding callbacks must run outside coordination,
   with explicit values frozen once rather than recomputed on conflict retries.
2. Stage uniquely named immutable generation data. Use canonical string payloads;
   any dense vectors and filter fields are derived projections. Failed/partial
   staging must never become visible to readers. Set explicit staging/collection
   size bounds before claiming a scalable design; whole-generation copies have
   real transfer/storage/write amplification costs.
3. Initialize a manifest without replacing an existing collection's manifest.
   For subsequent publication, investigate Qdrant 1.20.2 `update_only` combined
   with `update_filter` on a unique prior revision, `ordering=strong`, and
   `wait=true`. Keep the manifest in one explicitly configured shard. The probe
   verifies useful local behavior, not a complete compare-and-swap proof for
   replicas, leader changes, timeouts or collection deletion/recreation.
4. Confirm a unique publication token using an appropriately consistent read.
   Both matching and skipped writes can return completed. Classify conflicts
   separately from uncertain writes/replies; never replay or delete a possibly
   published generation because an acknowledgement/readback was lost. A
   successful publication followed immediately by another publication must not
   cause an earlier completed operation to be misclassified and replayed.
5. Readers pin one validated generation for the entire operation. Full canonical
   scanning with Go filters and `exactsearch` is the first ranking path. Keep old
   generations alive while readers or uncertain publications may reference them.
   Prove a cross-process lease/retention protocol or defer reclamation with
   documented bounds; TTL alone must not truncate a long-running reader.

If the backend cannot support those guarantees, keep Qdrant explicitly pending
and evaluate an explicit transactional coordinator or an additive API with
different semantics. Neither dependency nor contract change is authorized by a
capability checkbox. Do not silently introduce one in the connector.

## Ordered implementation gates

1. **Merged [PR #60](https://github.com/gabisonia/go-vectorstore/pull/60):** pinned
   real-server compatibility probe, design and CI; all 19 jobs passed.
2. **Merged [PR #61](https://github.com/gabisonia/go-vectorstore/pull/61):** executable manifest/generation
   protocol with independent clients, concurrency, stale revisions, logical
   delete/recreate, uncertain outcomes, read-only recovery and reader retention.
   One standalone auto shard/replica; finite lifetime slots/receipts; no online
   reclamation. Physical reset with active clients remains outside its contract.
3. **Provider foundation implemented:** bounded standalone snapshots, collection
   lifecycle, typed/dynamic CRUD, canonical dense ranking/filtering, embeddings and
   metadata. Reuse `vectordata` contracts/codecs and the existing Go engine. Shared
   conformance/filter parity, independent clients, delayed requests and process
   restart checks run on the real pinned server. See [limits](qdrant.md) and the
   [rerunnable Aspire sample](../samples/qdrant-store/README.md).
4. **Native execution:** separately prove numerical/candidate recall, filter truth,
   complete ties and one-generation payload retrieval; then measure against the
   canonical path. Sparse/hybrid support follows in later focused increments.

The provider matrix reports the bounded foundation's exact capabilities. Other parity-plan stages follow
the Qdrant decision rather than being silently marked complete.

## Primary references

- [Qdrant 1.20.2 release](https://github.com/qdrant/qdrant/releases/tag/v1.20.2)
- [Pinned REST schema](https://github.com/qdrant/qdrant/blob/v1.20.2/docs/redoc/v1.20.x/openapi.json)
  for point IDs, update modes/filters, operation replies, metadata and alias actions.
- [Upsert API](https://api.qdrant.tech/api-reference/points/upsert-points)
  and [consistency guarantees](https://qdrant.tech/documentation/scaling/consistency-guarantees/).
  Write ordering/read consistency are distinct from a multi-record transaction
  or a multi-request read snapshot.
- [Microsoft's Qdrant connector](https://learn.microsoft.com/en-us/semantic-kernel/concepts/vector-store-connectors/out-of-the-box-connectors/qdrant-connector)
  supplies context for connector breadth. Parity targets the shared workflows;
  it does not imply identical APIs or that every .NET connector has this
  repository's duplicate-insert, batch, distance or type semantics.
