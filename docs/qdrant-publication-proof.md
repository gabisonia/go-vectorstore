# Qdrant publication protocol proof

This is a bounded, executable experiment for the
[Qdrant connector design](qdrant-design.md), following merged
[PR #60](https://github.com/gabisonia/go-vectorstore/pull/60). It adds no public
provider, supported storage format or capability claims. The implementation lives
only in tagged test files: [protocol](../internal/qdrantprobe/publication_test.go)
and [real-server cases](../internal/qdrantprobe/publication_cases_test.go).

The experiment demonstrates guarded publication and read-only recovery on pinned
Qdrant **1.20.2**, using one node, one explicitly configured auto shard and one
replica, with cooperating clients. It does not establish distributed correctness,
crash/restart durability, operational capacity, native vector recall or performance.
Those remain separate provider acceptance checks.

## State and operation order

A dedicated physical collection contains a manifest at numeric point ID 0 and a
bounded set of disjoint generation slots. The manifest's authoritative state is
an opaque JSON string. Separate revision/epoch payload strings are used only as
conditional-write predicates and checked against the authoritative state on read.
Logical model keys and all record values use the existing shared `modelcodec`
inside canonical string payloads; no authoritative numeric value is parsed by
Qdrant. This proof does not create dense-vector projections or native indexes.

The manifest contains a unique revision, logical collection epoch, normalized
definition, active/tombstone state, current generation, generated-key counter,
append-only slot reservations, transition receipts and published generation tokens.

1. **Initialize:** validate the dedicated collection's topology/marker, then use
   `insert_only` for point 0 and adopt the winning manifest by readback. A delayed
   initializer cannot replace an existing active or tombstoned root. Initialization
   never resets a root, slots or receipt history.
2. **Prepare:** validate the entire explicit canonical record set in Go, including
   duplicate keys and vectors. Apply record/byte limits before remote reservation.
   Persisted definitions have no embedding callbacks. The provider must prepare
   actual mutations and embeddings once before attempting protocol operations.
3. **Reserve:** append a unique owner token and immutable descriptor through a
   revision/epoch-guarded `update_only` of the manifest. The descriptor includes
   lifetime slot index, logical epoch, expected prior head, count, byte length,
   length-delimited SHA-256 digest and proposed next-key state. Reservation happens
   before staging, so crashed/uncertain staging consumes a tracked finite slot.
4. **Stage:** confirm the reservation belongs to this exact descriptor before
   touching its point IDs. Write two-record batches using `insert_only`, then
   verify every ID, owner, epoch, count, canonical bytes and digest. Other slots
   are disjoint, and slots are never reused. A silent skip cannot validate foreign
   contents; partial staging cannot pass complete-generation validation.
5. **Publish:** read the manifest, check active epoch and expected prior head,
   validate the complete generation, then atomically replace the guarded manifest
   with the new head, counter, transition receipt and stable publication token.
   Concurrent reservations alone need not change the visible head, but a changed
   revision still produces a conflict; this experiment does not automatically retry.
6. **Confirm:** every transition retains its unique receipt. A later writer copies
   the full receipt history, so a superseded successful write is still confirmed.
   Published generation tokens also allow a fresh client to reconcile an unknown
   publication by read only, after the original request state is gone.
7. **Read:** pin one manifest/descriptor, fetch only that generation's immutable
   IDs across batches, and validate the entire generation before returning records.
   Subsequent publications do not redirect later batches to a new generation.
8. **Delete/recreate:** logical deletion tombstones the persistent root. Recreation
   assigns a fresh epoch, empty head and fresh key sequence while retaining all old
   slots and receipts. Stale publications/deletions cannot affect the new epoch;
   already pinned old readers can finish.

All writes use `wait=true&ordering=strong`; reads request `consistency=all`.
The topology and physical collection must remain fixed while clients are active.
The probe rejects a two-shard collection before initializing its root. The HTTP
boundary has cancellation/deadlines, bounded responses and no automatic write retry.

## Outcomes and uncertainty

Matching and skipped conditional writes can both return completed. The protocol
therefore uses readback, never the HTTP/update status alone, as evidence of a
matching transition.

| Observation | Result |
| --- | --- |
| Our retained transition receipt is visible, including after a later publication | Confirmed success |
| A fresh client finds our stable published-generation token | Read-only confirmation of publication |
| Completed conditional reply, readable different revision, no receipt for this transition | Confirmed conflict; no automatic retry |
| Lost/error/non-completed reply or failed confirmation, without positive receipt evidence | Unknown; preserve underlying error and retain data |
| Receipt absent while the original request may still be in flight | Unknown, even if the current head is unchanged |
| Missing/foreign/corrupt generation data | Error, with no partial result or head publication |

An unknown request can later commit, or it can lose to another writer and become
a no-op. Tests delay the **original request's delivery** until after the caller
has returned unknown, then exercise both outcomes. They do not retry that request.
An uncertain reservation cannot stage data until its exact ownership is confirmed;
this prevents it from writing another writer's slot after a delayed reservation
loses. Unknown outcomes never trigger deletion of potentially published data.

This proof provides no general request retry/idempotency API. A future provider
must expose useful unknown-outcome diagnostics and keep embedding/key allocation
semantics consistent; user retries cannot be assumed safe merely because an error
is returned.

## Retention, cleanup and hard bounds

Each nonempty snapshot registers its reader token against the observed manifest
revision before fetching records and releases it after the final batch. A
publisher that wins the race invalidates a pending registration against the old
revision. Registration and release each consume one of the 128 lifetime
receipts. A conflict, cancellation, exhausted receipt budget or lost response
can fail a snapshot. A failed release can leave its pin in place.

Targeted cleanup accepts a previously published generation whose successor has
become visible and whose last pin has gone. A published token can never become
head again, so registration of a new pin on the retired generation is fenced
even between the cleanup check and deletion. Cleanup deletes only that slot's
data points. Its descriptor, IDs, publication token and receipts remain reserved
for the lifetime of the collection. A lost deletion reply leaves the deletion
outcome unknown.

This requires every snapshot reader to register and each staging owner to
complete its sequential writes before publication. Unpublished slots, including
uncertain staging, are ineligible. Mixed clients that bypass registration fall
outside the proof. An abandoned pin retains its generation indefinitely. The
manifest admits up to 16 pins and the collection admits 16 lifetime slots.

The real-server cases pause A after its first batch, publish B, and observe
cleanup refuse A until its reader finishes. After release they verify removal
of A's points. A separate case discards A's publication reply and confirms A's
receipt after B publishes and A's data is removed. The counterexample that
deliberately bypasses the pin check breaks the second batch of A.

| Experiment bound | Value |
| --- | --- |
| Reserved generations per physical root, across all logical epochs | 16 lifetime slots |
| Records per generation | 32 |
| Canonical record bytes | 8 KiB |
| Total canonical bytes per generation | 64 KiB |
| Definition string | 8 KiB |
| Successful manifest transitions, including lifecycle/reservations | 128 retained receipts |
| Manifest string accepted by the reader | 128 KiB |

The fixed ID layout permits at most 512 data points plus point 0. Retained
canonical generation contents are bounded by 1 MiB; these are logical payload
bounds, **not** disk/WAL/RSS or Qdrant storage-overhead estimates. Stale/no-op
requests and server maintenance can still incur backend work/storage. The key
counter changes only with a published head, not on reservation or staging failure.

Exhaustion rejects further transitions/reservations before writing; it never
evicts success evidence or reuses a possibly referenced slot. The small constants
make exhaustive CI tests practical. They are not proposed production defaults and
do not establish a usable long-running capacity. Physical destruction/reset is
permitted only after external quiescence; it is not a library operation or a
procedure proven by this experiment. Admin deletion/recreation or reconfiguration
while clients remain active is outside this protocol's assumptions.

## Evidence and reproduction

The publication and compatibility cases use distinct HTTP clients/transports with no process-local publication
mutex. Fault wrappers intercept the client boundary of the real Qdrant server;
they never substitute a fake backend or fabricate a successful response.

Coverage includes competing initialization/reservations/publications, stale
revisions, superseding writes, dropped replies, unavailable readback, original
requests delivered late, conflicting slot ownership, partial staging, a publication
between reader batches, logical delete/recreate, corrupted ownership, cancelled
reads, input validation, receipt/slot exhaustion and topology rejection. The
128-receipt test performs real transitions rather than seeding invented history.

```sh
go test -count=1 -race -v -tags=qdrantprobe -timeout=3m ./internal/qdrantprobe
go test -count=10 -race -tags=qdrantprobe -run 'TestPublicationConcurrent|TestPublicationInFlight' -timeout=3m ./internal/qdrantprobe
go vet -tags=qdrantprobe ./internal/qdrantprobe
```

The suite owns/removes a pinned disposable container by default, or uses the
explicit disposable-server URL/key documented in the [design](qdrant-design.md).
CI runs both compatibility and publication tests on minimum/current Go. Normal
tests still need no Qdrant.

Source inspection informs the single-node assumption: pinned Qdrant's
[conditional submission fence](https://github.com/qdrant/qdrant/blob/v1.20.2/lib/collection/src/shards/local_shard/resolve_submit.rs)
drains earlier queued updates before resolving predicates and appending the
resolved operation to the WAL. The
[point retention logic](https://github.com/qdrant/qdrant/blob/v1.20.2/lib/shard/src/update/points/upsert.rs)
applies update modes, and
[strong-ordering handling](https://github.com/qdrant/qdrant/blob/v1.20.2/lib/collection/src/shards/replica_set/update.rs)
serializes leader updates. These are pinned implementation observations, not a
promise about other releases or distributed failures. Revisit them on upgrades;
the real-server tests do not replace a distributed proof or crash/replay tests.

## Provider foundation and remaining gates

The [provider foundation](qdrant.md) uses the guarded-manifest/receipt primitive
with the entire bounded canonical dataset in one point. It has its own immutable
configurable bounds, physical ownership, tombstones, outcome diagnostics and
process-restart tests; it does not adopt this experiment's slot layout/constants.
Typed/dynamic CRUD, exact dense ranking, filters, embeddings, metadata and shared
conformance run against real Qdrant. Native multi-point execution still needs
generation retention/reclamation and candidate recall proofs. Additional
topologies and sparse/hybrid support remain later increments.
