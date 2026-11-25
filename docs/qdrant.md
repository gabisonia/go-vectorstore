# Bounded Qdrant provider foundation

`stores/qdrant` implements the model API on **standalone Qdrant 1.20.2**, with one
auto shard and one replica per owned physical collection. It provides typed and
dynamic CRUD, all portable keys/data, generated int64/UUID keys, optional named
dense float32/float16 vectors, embeddings, portable filters, metadata, iterators
and exact Go ranking. Binary/sparse vectors, native indexes, full-text/hybrid and
migrations are not implemented. Accepted index hints do not create native indexes.

This is a bounded foundation, not a distributed or unbounded-write connector.
Choose PostgreSQL for the existing server-side ANN/hybrid workflow. Normal library
tests need no Qdrant; real-server checks use the integration build tag.

## Native C# collections

Since v0.6.0, `qdrant.AttachNativePoints[K]` opens externally managed
Qdrant collections by physical name. Choose `uint64` or `uuid.UUID` for the
point ID and supply the complete map of named dense cosine vectors to their
dimensions. `UpsertPoint` requires all configured vectors and replaces the full
payload. `GetPoint` retrieves the vectors and payload. `Search` selects a named
vector and accepts a skip offset and minimum similarity. `SearchKeyword` uses a
keyword index. `HybridSearch` accepts an indexed text field and fuses its keyword
matches with the named vector search using Qdrant's reciprocal rank fusion.

A lost write acknowledgement returns `ErrNativeOutcomeUnknown`. The native
handle operates on individual points and uses the server's ranking and scores.
The bounded model provider described below uses canonical snapshots.
Run `bash scripts/test-qdrant-parity.sh` for the Docker cross-language tests.

## Storage and consistency

One physical collection is deterministically named from SHA-256 of the namespace
and logical name. Its metadata records the full names, format and immutable
limits. Every operation validates server version, standalone mode, physical
ownership and topology. Foreign collections/configurations are rejected without
initializing or replacing their contents. Physical configuration must remain
fixed while clients or uncertain requests exist.

Point 0 holds one opaque canonical JSON snapshot: definition, epoch, revision,
records, generated-key sequence and retained transition receipts. Record envelopes
reuse the shared codec, preserving exact integers/decimals, null distinctions and
unmodified vectors. No authoritative number is parsed through Qdrant payload
numeric fields. Filters and distances use the existing canonical Go engine.

Reads fetch the complete snapshot in one consistent point retrieval. Once copied,
a reader needs no historical backend points, so concurrent writes or logical
deletion cannot redirect later enumeration to another state. Iterators buffer a
snapshot/page, release HTTP resources, and check cancellation while yielding.

Writes freeze/prepare embeddings once outside coordination, then conditionally
replace the snapshot using the prior revision/epoch, `update_only`, `wait=true`
and strong ordering. Confirmed revision conflicts may retry at most 64 times;
transport errors, cancellation or uncertain acknowledgements are never replayed.
Generated integer keys commit with the records; failed batches consume none.
A logical operation cannot cross an epoch change while its embedder runs.

This reuses the guarded-manifest/retained-receipt primitive established by the
[publication proof](qdrant-publication-proof.md), with the entire bounded canonical
dataset in the manifest. It does not promote the experiment's multi-point slot
layout or tiny constants into a production format. Native multi-point execution
will require separate immutable-generation retention and recall proofs.

## Explicit capacity and lifetime

| Limit | Default | Configurable maximum |
| --- | --- | --- |
| Records per snapshot/batch | 4,096 | 65,536 |
| Canonical record/key bytes | 4 MiB | 16 MiB |
| Retained receipts per physical collection | 16,384 | 262,144 |
| Definition JSON | 64 KiB | Fixed |
| Encoded HTTP request/response | 64 MiB | Fixed |

`StoreOptions.Limits` selects record/byte/receipt limits before creating a
collection; clients must use the same values on reopen. The byte budget excludes
definition, receipts, JSON wrapping and server storage overhead. Request limits
are checked before sending; response/canonical limits are checked before decoding.
Configure server request/storage budgets for the selected limits. These logical
bounds are not disk, WAL, RSS, latency or throughput estimates.

Initialization and lifecycle transitions consume receipts, as do changed writes.
No-op record updates/deletes consume none. Receipts survive logical deletion and
recreation. Exhaustion returns `ErrLimit` before a mutation; reads remain usable.
There is no online eviction, automatic reset or reclamation. Size the lifetime
budget deliberately; this connector is unsuitable for indefinite write workloads
without an externally quiesced migration/reset procedure. Such administration is
outside the provider API and is not a safe retry for an unknown write.

`store.Usage(ctx, name)` reports active/tombstone state, record count, canonical
bytes, retained receipts and configured limits without exposing record contents.
Use it to monitor remaining capacity before exhaustion; it is an observation,
not a reservation or disk/memory estimate.

`DeleteCollection` is a logical tombstone. Recreation gets a new epoch and key
sequence while retaining receipts. Never physically delete/reconfigure an owned
collection with active clients or delayed/uncertain requests. Runtime permissions
must cover the actual read/write APIs; bootstrap additionally creates collections.
Use verified HTTPS, an application secret for API keys, bounded request contexts
and an explicit standalone deployment. The endpoint must identify one fixed
standalone server; independent nodes behind a load balancer are outside this
contract. Custom HTTP transports must not replay
writes. `Close` releases owned idle connections and rejects subsequent I/O;
caller-supplied clients/transports remain caller-owned.

## Unknown write outcomes

Use `errors.As` through `vectordata.OperationError` to obtain
`*qdrant.OutcomeUnknownError`. Its `Token` identifies a retained transition receipt;
`errors.Is(err, qdrant.ErrOutcomeUnknown)` and the original transport/context cause
remain discoverable. HTTP errors expose `*qdrant.HTTPError.StatusCode`, without
document bodies. Operation errors include configured system/database/namespace.

Call `store.ConfirmWrite(ctx, collectionName, outcome.Token)` from the same or a
fresh client. A retained receipt proves success, including after superseding
writes, logical recreation or the tested process restart. A missing receipt still
returns unknown: the original request might arrive later. Confirmation performs
only reads. Do not repeat an Insert with generated keys merely because the caller
received an error. `ErrConflict` indicates exhausted confirmed-conflict retries;
it is distinct from an uncertain write.

## Verification and operational scope

```sh
go test -race ./stores/qdrant
go vet ./stores/qdrant
go test -count=1 -race -tags=integration -timeout=5m ./stores/qdrant
go vet -tags=integration ./stores/qdrant
```

The integration suite owns/removes a pinned disposable server by default. For
an existing disposable server, use `QDRANT_TEST_URL` and optional
`QDRANT_TEST_API_KEY`; only unpredictable test namespaces are cleaned up. Supplied
servers are never restarted. The two restart cases run only on the owned fixture:

```sh
env -u QDRANT_TEST_URL -u QDRANT_TEST_API_KEY \
  go test -count=1 -race -tags=integration -run TestIntegrationRestart -timeout=3m ./stores/qdrant
```

Coverage includes shared conformance, deterministic filter parity, embeddings,
independent concurrent clients, sequence/reopen behavior, atomic invalid batches,
receipt exhaustion, unsupported topology/ownership, paused snapshot reads and
original requests delivered after returning unknown. Crash tests send SIGKILL to
the owned server process, verify exit 137, restart the same data and reopen with
fresh clients; they check records, sequences, receipts and tombstones. They do
not establish power-loss durability, disk corruption recovery, distributed
failover, backups/restores, native recall or performance. Minimum/current-Go CI
checks the provider plus separate owned-container restart cases.

Try the [rerunnable typed sample](../samples/qdrant-store/README.md) with the
[Aspire services](../samples/aspire-services/README.md). Keep deployment backup,
restore drills, capacity monitoring and unknown-write reconciliation explicit;
the API does not provide operational guarantees beyond its documented scope.
## Typed retrieval from native points

After attaching with `AttachNativePoints`, pass the handle, default named
cosine vector and a point-to-model decoder to `NewNativeVectorSearcher`.
The resulting `vectordata.VectorSearcher[T]` accepts physical vector and
payload field names. It supports top, skip, one indexed string equality and
optional vector projection. The decoder receives the point ID and full payload
in the ranked response. With `IncludeVectors` it also receives all stored
named vectors. Score is Qdrant cosine similarity and distance is one minus
score. Qdrant controls tie ordering. Thresholds and provider options return
`vectordata.ErrUnsupported`.

The [shared retrieval example](../stores/qdrant/native_searcher_test.go) runs
with `go test ./stores/qdrant -run TestMemoryRankedGuides`. Run
`bash scripts/test-qdrant-parity.sh` to exercise the same model, query and
rank assertions on its disposable Qdrant server. The
[native setup](../stores/qdrant/native_searcher_parity_test.go) creates the
physical schema and maps ranked points to the application model.
