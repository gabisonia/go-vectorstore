# Redis model provider

`stores/redis` implements model collections on standalone Redis. The default
uses atomic collection snapshots with exact dense search and portable filtering
in Go. Opt-in `NativeIndexes` adds Redis Search HNSW/FLAT indexes and scalar
filtering and string full-text indexes over immutable HASH/JSON generations.
Opt-in `StorageJSON` uses immutable RedisJSON documents with Go exact search or
native vector indexes.
It is available on master after v0.4.1. The default encoded collection limit is
16 MiB; writes still process the whole collection.

```go
store, err := redis.New(redis.StoreOptions{
    Addr: "localhost:63799", Namespace: "myapp", DB: 0,
})
if err != nil {
    return err
}
defer store.Close()
products, err := vectordata.EnsureTypedModelCollection[string, Product](
    ctx, store, "products", nil, vectordata.ModelCollectionOptions{},
)
```

Import `stores/redis` and `vectordata` from this module. Use the normal typed or
dynamic model APIs. See the complete [Redis sample](../samples/redis-store/README.md).
`New` and `GetModelCollection` perform no I/O. Ensure operations create/validate
storage; reads return errors when Redis is unavailable. Close releases the owned
go-redis client without deleting collections. This constructor supports a single
standalone address, not Redis Cluster, Sentinel or read-replica routing.

## Supported model behavior

- String, int64, uint64 and UUID keys; generated int64 and UUID keys.
- All 20 portable data types, native arrays, JSON paths and exact decimals.
- Multiple dense float32/float16 vectors, optional vectors and vector projection.
- Cosine, L2, squared L2, inner product and L1 exact search, with the shared score,
  threshold, filter, key tie-breaking and paging contracts.
- Text/custom embedding inputs and store/collection/property defaults.
- Lazy read iterators that buffer snapshots/pages and release client connections
  before callbacks and typed codecs run.
- Local capability/identity metadata and structured operation errors.

Binary/sparse vector definitions return `ErrUnsupported`. Without `NativeIndexes`,
index hints are accepted for portability and build no indexes. Native mode honors
string full-text hints and supports dense RRF hybrid search. Standalone keyword
queries and binary/sparse hybrid search remain unsupported. Unknown search
`ProviderOptions`
return `ErrUnsupported`. Migration planning and
application are not advertised. Runtime embedders are never persisted or invoked
while reconstructing stored vectors.

## Storage layouts and explicit upgrade

`StoreOptions.StorageMode` chooses the layout for **new** collections:
`StorageSnapshot` (the default) stores the original format-1 envelope;
`StorageHash` creates a format-2 manifest and immutable per-record hashes;
`StorageJSON` creates a format-3 manifest and immutable RedisJSON documents.
Existing collections keep their layout regardless of the constructor option.
All three support the same public model calls and Go exact search.
`NativeIndexes: true` selects `StorageHash` when `StorageMode` is unspecified;
explicit snapshot mode with native indexing is rejected. JSON native mode requires
`StorageMode: StorageJSON` explicitly.

```go
store, err := redis.New(redis.StoreOptions{
    Addr: "localhost:63799", Namespace: "myapp", StorageMode: redis.StorageHash,
})
```

To upgrade an existing collection, obtain its concrete handle using the same
registered model definition, then explicitly call:

```go
base, err := store.GetModelCollection("products", definition, vectordata.ModelCollectionOptions{})
if err != nil {
    return err
}
if err := base.(*redis.Collection).MigrateToHashStorage(ctx); err != nil {
    return err
}
```

The upgrade preserves schema, records and generated sequence, validates records
before staging, and publishes atomically under concurrent writes. Repeating it
after success is a read-only no-op. New clients reopen all three layouts; older
format-1-only binaries reject format 2, and pre-RedisJSON binaries reject format 3.
Hash conversion supports format 1 only; JSON-to-hash conversion is unsupported. This is
provider storage conversion, separate from model-schema migration capabilities.
Snapshot compatibility is checked against an immutable fixture from merged
commit `950401c`; SQLite's format is unchanged.

Format-2 hashes expose `__vs_payload` (canonical record JSON), `__vs_token`
(canonical key token) and `__vs_revision` (generation). String/int64/float64/bool
data fields use `d_` plus the base64url logical name. Native NULL omits that field;
other types remain in the canonical payload. Present dense vectors use `v_` plus
the encoded name, with little-endian FLOAT32 bytes; float16 values are already
rounded before projection. Absent vectors have no blob. Int64 projections use
exact decimal text. Native indexing adds separate high/low 32-bit NUMERIC fields
for exact integer ranges. The layout is internal, not an application write API.

Plain `StorageHash` needs no Search module and creates no indexes. Native mode
adds optional index metadata to the same format-2 manifest; earlier hash readers
ignore these fields. A writer without native indexing can publish a plain hash
generation again. Native readers then use Go fallback until a native write or
explicit rebuild. Older hash writers may leave obsolete indexes for pruning.

## RedisJSON storage

`StorageJSON` requires the RedisJSON module, tested on standalone Redis 8.2.
Database numbers other than zero are supported without native indexes. Constructor/handle discovery is
local; creating even an empty JSON collection checks module availability before
staging. Missing JSON commands return `ErrUnsupported` with the native Redis
cause and leave the source collection untouched. Ordinary Redis 7.4 remains
supported for snapshot/hash layouts without modules.

```go
store, err := redis.New(redis.StoreOptions{
    Addr: "localhost:63799", Namespace: "json_app", StorageMode: redis.StorageJSON,
})
```

A string manifest retains the schema, revision, generated sequence and sorted key
tokens. Each record is a real RedisJSON object written with `JSON.SET`, containing
three strings: `__vs_revision`, `__vs_token` and `__vs_payload`. The payload holds
canonical record JSON as an opaque string. RedisJSON and Lua parse only the wrapper,
so uint64 keys, exact decimals, arbitrary JSON numbers (including values beyond
float64 range), SQL NULL/JSON null and vector precision retain the shared codec's
semantics. Records are not exposed as application-owned JSON documents or writable
JSON paths; internal keys and field names are reserved.

Set `NativeIndexes: true` together with `StorageMode: StorageJSON` to build
HNSW/FLAT vector indexes on database zero with Redis Search and RedisJSON. Register
eligible vector index hints as shown below. Each wrapper then adds
`__vs_vectors`, containing bounded FLOAT32 arrays under internal aliases (`v0`,
`v1`, ...). Float16 models are rounded before projection; the opaque canonical
payload remains authoritative for returned data and exact distance/score units.
JSON indexes use a separate scoped prefix and `ON JSON` schema. Revision and
vector paths are `$.__vs_revision` and `$.__vs_vectors.vN`. Internal aliases keep
logical property names out of JSONPath syntax. Earlier format-3 readers ignore
additional wrapper fields; unknown index versions use the canonical Go path.

JSON native mode advertises the same 16 vector tuples and indexed
string/int64/float64/bool types as HASH native mode. Index metadata **version 4**
adds `__vs_scalars` projections: literal hex-encoded TAG values, explicit presence
markers and high/low 32-bit NUMERIC components for int64 data and integer keys.
Full 64-bit values and float64 equality values never enter RedisJSON/Lua numeric
parsing. Split components fit exactly in their numeric representation. Scalar
paths use internal names such as `$.__vs_scalars.__vs_tag_0`, separate from vectors
and canonical payloads. TAG fields explicitly use comma separators and case
sensitivity; hex tags contain no commas. Physical readiness checks validate paths,
types, separators and other observable index settings. See Redis's
[TAG defaults](https://redis.io/docs/latest/develop/ai/search-and-query/advanced-concepts/tags/).

Eligible filtered Query, Count and HNSW/FLAT searches use the existing native
filter compiler, including SQL NULL/UNKNOWN logic, exact equality and integer
ranges. Count returns an index count without fetching/decoding canonical payloads;
Query downloads all matching payloads and sorts/pages in Go. HNSW selects native
candidates; FLAT fetches every matching vector-bearing wrapper and remeasures in
Go for exact ordering, including ties and float roundoff. Missing/unready indexes,
unsupported filters or unsafe vectors, and `SearchTuning{Exact: true}` retain the
canonical Go fallback. Unfiltered Query and Get/GetMany still load the complete
generation. Native filtering/KNN, readiness/physical-schema checks and selected
`JSON.GET` wrapper reads share one Lua execution. Only outer wrappers are parsed;
canonical payload strings remain opaque.

Existing **version-3 vector-only indexes** retain their original schema and native
unfiltered vector search. Their Query, Count and filtered searches continue through
Go until an explicit `Collection.RebuildNativeIndexes` or native write publishes
version 4. Reads never rebuild indexes. Rebuild preserves the registered model,
records and generated sequence; adding a previously absent index hint still changes
the model definition. Older native clients that do not recognize version 4 fall
back to Go. Plain JSON writers publish an unindexed generation, while older native
writers can publish vector-only version 3; newer readers fall back appropriately.
Opening existing plain JSON storage also does not build indexes implicitly.

JSON full-text hints now build **index metadata version 5** with raw string
projections under `$.__vs_text.textN`, separate from scalar TAGs and vectors.
NULL text omits its projection; empty strings and embedded NUL/Unicode values
retain their original values. Models without full-text hints keep version 4.
Existing version-3/4 schemas remain readable until an explicit rebuild/native
write adds TEXT. Scalar version-4 reads stay native, and version-3 filters still
fall back to Go. Canonical payloads, registrations and storage format 3 are
unchanged. Plain/older writers remain compatible and can publish earlier index
versions again. See [full-text indexing](#full-text-indexing) for tokenizer,
validation and limit details. Dense hybrid search uses these indexes; standalone
keyword queries remain absent; KeywordTerms lists are supported.

To convert an existing snapshot or hash collection, use its unchanged registered
definition and call:

```go
if err := base.(*redis.Collection).MigrateToJSONStorage(ctx); err != nil {
    return err
}
```

Conversion validates all records, stages a complete new generation and publishes
schema/data/generated sequence in one final manifest SET. Explicit conflicts retry
against the latest source, including concurrent writes through another client.
Native mode builds and waits for the new JSON vector index before publication.
Repeating conversion after success is a read-only no-op. Successful conversion
retires previous documents/indexes best-effort; uncertain replies preserve
possibly committed JSON data and require state reconciliation before an application
retry. Use pruning to reclaim old auxiliary data if cleanup was interrupted.
There is no JSON downgrade operation. This is provider storage conversion;
model-schema migration planning/application remain unsupported.

JSON generations use separate, namespace/collection-scoped key prefixes. A unique
one-hour lease protects staged documents, which also receive one-hour TTLs in
pipeline batches of 128. Publication checks the lease, revision, key tokens and
payload presence, removes TTLs and performs the final SET. Earlier script errors
can leave auxiliary documents but cannot publish a partial logical batch. Reads
verify the exact manifest and lease and fetch/validate every referenced wrapper in
one read-only Lua execution. Changed manifests retry before returning any rows;
missing/damaged referenced documents fail the read. Native writes build and wait
for index readiness before the final SET, preserving uncertain publications and
retiring old indexes only after acknowledgment. Canonical decoding and caller
callbacks run after the client connection is released.

`store.PruneJSONStorage(ctx, name)` reclaims obsolete non-expiring JSON documents
and leases, including after deletion. It guards deletion with the current manifest,
protects current generations and active TTL staging, and returns a Redis-key count.
Concurrent changes can leave work for a later pass. `DeleteCollection` attempts both
hash and JSON cleanup after deleting the logical collection. `PruneHashStorage`
and `PruneNativeIndexes` can reclaim obsolete data/indexes after a JSON
conversion with a lost reply. No cleanup worker runs in the background.

## Native indexes and search

Native mode requires standalone Redis with Search, tested on Redis 8.2, and
database zero. The constructor rejects nonzero databases; it does not probe the
server. Missing Search fails index creation without publishing the collection.
Ordinary Redis 7.4 continues to support snapshot/hash modes.

```go
store, err := redis.New(redis.StoreOptions{
    Addr: "localhost:63799", Namespace: "native_app", NativeIndexes: true,
})
// On a new definition, before obtaining/ensuring the collection:
definition.Data[0].IsIndexed = true
definition.Vectors[0].Index = &vectordata.VectorIndexOptions{
    Method: vectordata.IndexMethodHNSW,
}
```

Register the same definition when reopening. Enabling the store option does not
silently convert existing storage or change its registered definition. For an
existing format-1 collection, `MigrateToHashStorage` builds native indexes during
conversion; `MigrateToJSONStorage` builds JSON vector indexes in native mode.
For an existing hash/JSON collection with matching index hints, call
`base.(*redis.Collection).RebuildNativeIndexes(ctx)` explicitly. Repeating the
storage conversion on the target layout is a no-op, so it does not rebuild indexes.

Select exhaustive native search on a new model with
`VectorIndexOptions{Method: vectordata.IndexMethodFlat}` or the struct tag
`vectordata:"vector,dimensions=16,method=flat"`. FLAT and HNSW properties
may coexist; select one by logical `VectorProperty` name. FLAT accepts no HNSW or
IVFFlat construction parameters. It is distinct from pgvector's approximate
IVFFlat index. Memory/SQLite accept FLAT hints and keep exact Go search;
PostgreSQL rejects them before database access. Definitions containing `flat`
require a binary that recognizes this new method. Existing HNSW registrations
and storage formats are unchanged. Rebuild preserves the registered method;
it does not migrate a collection between HNSW and FLAT.

Capabilities are local configuration metadata, not a module/permission probe:

- HNSW and FLAT support dense float32/float16 models with cosine, L2, squared L2
  and inner product: eight tuples per method (16 total), up to 4,096 dimensions.
  Float16 values are
  rounded before FLOAT32 projection. HNSW `M` is at most 512 and
  `EfConstruction` at most 1,000,000. L1, IVFFlat and other unsupported hints keep
  Go exact search.
- Nonzero vector components must have absolute values in `[1e-10, 1e10]` for the
  native path. Any unsafe stored vector omits that vector field from the entire
  generation's index; an unsafe query also uses Go exact search. The canonical
  values remain intact.
- On HASH and JSON storage, keys support equality, `In`, existence and int64/uint64 ranges. Indexed string,
  int64, float64 and bool data properties support equality, `In` and existence;
  indexed int64 also supports ranges. Integer ranges retain all 64 bits through
  split fields. Literal TAG values use hex encoding, including punctuation and
  empty strings; float equality preserves signed-zero equivalence.
- `And`, `Or` and `Not` retain SQL NULL/UNKNOWN semantics. Unsupported filters,
  including float/string ordering, JSON, arrays, calendar/decimal values and
  unindexed data, fall back as a whole. Large literals (over 511 bytes after key
  tokenization), `In` lists over 256, deep/large trees and excessive parameters
  also use Go evaluation. Native schemas allow at most 512 physical fields.

Native filtered Query on HASH/JSON downloads matching canonical records, validates them and
sorts/pages in Go. Native Count returns the index count without decoding record
payloads or validating their vectors. HNSW selects an enlarged candidate set,
then recomputes distances/scores and applies thresholds, stable key ordering and
paging in Go. Candidates expand when a page underfills or a boundary tie remains;
unresolved ties/underfill use the full exact snapshot. ANN recall otherwise remains
approximate. Missing/not-ready indexes fall back; physical schema drift and
indexing failures return errors. Filters outside the native subset use exact search.

FLAT asks Redis for every matching vector, then performs bounded exact selection
in Go. This preserves the canonical Go result ordering even when Redis's FLOAT32
distance arithmetic rounds distinct distances to the same number. Ties, optional
vectors, underfilled/empty pages and thresholds do not require HNSW's candidate
expansion or a tie fallback. Selective indexed filters reduce returned payloads;
unfiltered FLAT transfers all vector-bearing records. It is an exhaustive scan,
not an ANN latency improvement. Both native paths keep the existing fallback
for missing indexes, unsupported filters and unsafe vector magnitudes.

```go
opts := vectordata.ModelSearchOptions{
    ProviderOptions: redis.SearchTuning{EFRuntime: 200},
}
// For a full-snapshot exact reference instead:
opts.ProviderOptions = redis.SearchTuning{Exact: true}
```

For HNSW, `EFRuntime` defaults to 128; accepted values are 0 through 10,000.
Positive values require native mode, are rejected for a selected FLAT property
before I/O/embedding, and cannot be combined with `Exact`. FLAT sends no HNSW
runtime parameter. `Exact: true` bypasses native execution for either method.
Options are passed by value. Distances, score units and thresholds retain the
public model contract.

## Full-text indexing

With `NativeIndexes: true`, mark a string property with `IsFullTextIndexed` or
`vectordata:"data,fulltext"` to create a Redis `TEXT` field. Native capability
metadata for HASH and JSON reports `FullTextIndexing: true`; plain snapshot/HASH/JSON
modes keep it false. Non-string full-text definitions are rejected by model validation.

```go
definition.Data[0].IsFullTextIndexed = true
// IsIndexed can also be true for independent exact TAG equality/IN filtering.
```

HASH TEXT reuses the raw string projection; JSON stores a separate `__vs_text`
string map with safe paths such as `$.__vs_text.text0`. TEXT has its own alias,
separate from hex-encoded scalar TAG fields. NULL omits the projection; an empty
string is retained but produces no text tokens. String values, including Unicode
and embedded NUL bytes, remain intact in canonical records. A TEXT-only property
does not enable scalar filter pushdown: Query/Count equality still compares the
whole literal through Go fallback unless `IsIndexed` is also true.

Creation selects English language, stemming and weight 1, retaining Redis's
standard punctuation/case tokenization and default stopwords. No custom language,
weights, phonetics or stopword options are exposed. Validation checks field
identity/type, weight, stemming/index flags, reported index options and custom
stopword lists before publication and native reads. FT.INFO does not expose every
text setting (including language/phonetics on Redis 8.2); external index edits are
unsupported. See Redis [tokenization](https://redis.io/docs/latest/develop/ai/search-and-query/advanced-concepts/escaping/)
and [index options](https://redis.io/docs/latest/commands/ft.create/).

There are at most 128 TEXT fields on the tested 64-bit Redis 8.2 server, within the
provider's overall 512-field limit. Native handles reject larger definitions
locally. These indexes participate in the same immutable generation, readiness,
publication, retirement and pruning rules as vector/scalar indexes. Every indexed
HASH/JSON write also rebuilds the text index; JSON projection bytes count toward
the generation budget, while Redis text-index allocator memory is outside
the encoded byte budget.

HASH full-text generations use **index metadata version 2**, separate from
collection storage format 2. Version-1 indexes retain their scalar/vector schema,
including previously ignored full-text hints. JSON full-text generations use
**index metadata version 5** with storage format 3; version-3 vector-only and
version-4 scalar/vector schemas retain their original fields. Reads do not rebuild
indexes. `RebuildNativeIndexes` or the next native write adds TEXT without changing
registration, canonical records or generated sequence. Collections with no
full-text hints keep HASH version 1 or JSON version 4.

Plain readers can read these storage formats, and older native clients that do not
recognize the new index version use their full-snapshot fallback. Older writers
can publish earlier index versions again; a current native write/rebuild restores
TEXT. Existing storage conversions follow the explicit operations above. Adding a
previously absent full-text hint changes the registered model definition; rebuild
does not perform that model-schema migration.

This feature creates and validates physical indexes. `SearchText` and `SearchInput`
still generate embeddings for vector search; hybrid APIs explicitly combine keyword
and vector candidates. `HybridSearch` and `HybridKeywordTerms` are true only in
native mode. Internal index names/field aliases remain private.
See the [full-text sample mode](../samples/redis-store/README.md).

## Hybrid search

Native HASH and JSON collections implement `HybridSearcher`, `InputHybridSearcher`
and their lazy buffered iterators. Mark a string with `IsFullTextIndexed` and use
a dense float32/float16 vector property. Select `TextProperty`/`VectorProperty`
when the definition contains more than one eligible property. Typed wrappers use
the same contract:

```go
hits, err := collection.HybridSearch(ctx, []float32{1, 0, 0}, "storage choices", 5,
    vectordata.HybridSearchOptions{
        TextProperty: "Title",
        CandidateLimit: 100,
        ModelSearchOptions: vectordata.ModelSearchOptions{
            VectorProperty: "Vector",
            Filter: vectordata.Eq(vectordata.Column("Category"), "guide"),
        },
    })
```

Keywords are plain UTF-8 text without NUL, limited to 16,384 bytes and 128 tokens.
Letters, numbers, combining marks and underscores form bound TEXT operands;
other characters separate tokens. Tokens are combined with AND. Quotes, `|`,
`-`, `*`, field syntax and backslashes do not expose Redis query operators.
Punctuation-only input contributes no keyword candidates; blank input is invalid.
Redis applies its English stemming/default stopwords and the explicit `BM25`
scorer supported by Redis 8.2. Keyword scores differ from PostgreSQL's text ranking.
See Redis [scoring](https://redis.io/docs/latest/develop/ai/search-and-query/advanced-concepts/scoring/).

`HybridSearchOptions.KeywordTerms` replaces the positional string when non-nil.
Pass blank positional keywords and a nonempty list: `[]string{"cloud costs",
"caching"}` matches `(cloud AND costs) OR caching`. Groups use the same literal
token rules; punctuation-only groups add no keyword candidates. Blank entries,
invalid UTF-8, NUL and conflicting positional keywords return
`ErrInvalidSearchOptions` before I/O/embedding. Lists are limited to 128 groups,
128 aggregate tokens and 16,384 aggregate bytes. Nil preserves positional AND
matching; a non-nil empty list is invalid. Every iterator enumeration snapshots
the current list before callbacks; avoid concurrent mutation during enumeration.
No schema upgrade is needed for existing ready TEXT generations.

`CandidateLimit` defaults to `max(100, top + Skip)`, must cover the requested page,
and is capped at 1,000,000. Each candidate list is ranked after filtering and
excluding records without the selected vector. Vector ties, keyword-score ties
and fused-score ties use native key order. Fusion adds `1/(RRFConstant + rank)`
for each list containing a record, with one-based ranks and default constant 60.
Results report `ScoreKind=ScoreRRF` and the canonical raw vector distance.
Distance and fused-score thresholds apply after fusion, followed by paging.
RRF scores are not vector similarity scores.

Both index searches and their canonical payload union execute in one Lua guard
with exact manifest, lease, physical-schema and readiness checks. A changed
manifest retries the complete read. Keyword matches are all retrieved so Go can
apply unsupported filters, exclude optional missing vectors and resolve ties
before the keyword candidate limit. Supported scalar filters are also pushed
into both Redis searches. No partial keyword result set is accepted when a
server result cap prevents complete ranking.

HNSW oversamples vector candidates and retains approximate recall; unresolved
boundary ties/underfill use a fresh guarded exact scan of the same complete read.
FLAT retrieves all matching vectors for exact Go remeasurement before limiting.
`SearchTuning{Exact: true}`, unsupported vector-index tuples, unsafe magnitudes
and unsupported filters use the full canonical generation for vector ranking.
The keyword side remains native in these cases. Provider tuning follows ordinary
vector search, including rejection of positive EF_RUNTIME on FLAT.

Hybrid search requires a ready TEXT generation (HASH metadata 2 or JSON metadata
5). Plain storage, old scalar/vector-only indexes and missing/unready indexes
return `ErrUnsupported`; schema drift/damage returns the
existing schema/record errors. No read creates or rebuilds indexes implicitly.
Use an explicit rebuild with the unchanged full-text-marked definition when
upgrading an old index. Invalid options/filters/keywords are rejected before I/O
or embedding. Text/custom-input forms verify index availability before embedding,
snapshot keyword lists and filter/threshold values, release connections before
callbacks, and embed once even when publication conflicts require read retries.

`HybridSearchText` and `HybridSearchInput` embed query input; their keyword string
is independent. Iterators buffer a complete page, release Redis resources before
yielding, and preserve cancellation/early-stop behavior. Writes still replace the
entire generation/index. Hybrid reads retain the O(N) manifest, retrieve all keyword
matches, and may scan all canonical records. Measure target workloads before
assuming native hybrid reduces transfer or latency. See the rerunnable
[hybrid sample](../samples/redis-store/README.md).

## Atomicity and concurrency

Each collection has a Redis string at an internal key containing an encoded
namespace and collection name. Format 1 embeds schema, sequence and all records;
format 2 references an immutable hash generation, and format 3 references an
immutable RedisJSON generation. Keys and data
are decoded by schema, preserving uint64/JSON/decimal precision and native NULL
versus JSON null. Redis Lua never parses these numbers. SQLite shares the canonical
record codec; its existing format-1 bytes remain compatible.

Writes validate and embed the entire input before mutation, stage a new snapshot
in Go, then execute a parameterized compare-and-swap script. The script compares
the old bytes and publishes with one final `SET`. There are no intermediate write
commands requiring rollback in the snapshot layout. Hash storage first claims a
unique generation lease and stages hashes with one-hour TTLs, in pipeline batches
of 128. The publication script checks all staged records, removes TTLs from the
new generation, then performs the final manifest SET. Failures before SET leave
the old logical records/sequence intact, although auxiliary staging keys may remain.
A fresh revision distinguishes a recreated collection
from an older snapshot. See Redis's [script execution semantics](https://redis.io/docs/latest/develop/programmability/eval-intro/).

Only an explicit conflict reply retries, at most 64 attempts with a context-aware
delay of 1–10 ms. Each attempt reloads and checks schema/format. Embedding callbacks
and generated record UUID allocation occur once per operation. Failed validation, duplicate keys,
sequence exhaustion and size-limit failures leave records and sequence unchanged.
Explicit keys do not advance the integer sequence; generated-key collisions fail
even during Upsert. Network/timeout/Redis errors return immediately, and client
automatic retries are disabled. A lost successful reply can mean the entire batch
committed; reconcile state before deciding whether to retry at the application level.

A snapshot-layout read obtains the collection with one GET. Hash reads fetch a
manifest, then verify those exact bytes and read its payloads in one side-effect-free
Lua execution. A changed manifest retries the read, at most 64 attempts; missing or
damaged referenced hashes fail the whole read. Retiring the previous generation
cannot race this atomic read. Concurrent batch writes cannot produce mixed
results. Go query/filter/search evaluation happens
after the connection is released. Get/GetMany validate only requested records;
Default/fallback Query, filtered Count and Search decode/validate the full snapshot.
Default nil-filter Count parses the envelope but skips record validation. QueryIter discards the page
on a decode/validation error before yielding any records. ListCollectionNames uses
deduplicated/sorted `SCAN` results; concurrent create/delete is not a snapshot of
the namespace. None of the layouts promises incremental result decoding.

Native writes build a separate index for each immutable generation and wait for
the exact document count, zero indexing failures and the expected physical schema
before publication (at most 30 seconds, bounded by caller context). Failure leaves
the old manifest/sequence intact. Native reads verify the manifest, lease and
index readiness and execute FT.SEARCH in one Lua execution. A concurrent change
retries before returning any rows; retiring an old generation cannot mix reads.
FT.SEARCH uses `TIMEOUT 0` to prevent partial timeout replies. Inside Lua it runs
synchronously on Redis's main thread; a client deadline can return while server
work continues. Reserve all internal keys/index names for this library. Native
Count trusts the immutable index rather than detecting arbitrary external payload
edits; external edits/eviction are unsupported.

## Hash staging and cleanup

Known failed/conflicting staging is cleaned up when possible. After acknowledged
publication, previous generations are retired on a best-effort basis. Cleanup
failure never changes the successful logical result. Lost replies or process
failure can leave obsolete non-expiring keys, including a partially persisted
staging generation. Published data never depends on expiring staging keys.

Call `store.PruneHashStorage(ctx, collectionName)` to reclaim obsolete non-expiring
auxiliary keys, even after collection deletion. It checks the current manifest
atomically with deletion, protects the current generation, and skips TTL staging
that may belong to an active writer. It returns Redis keys removed, including
leases, rather than record count. Concurrent changes may leave work for a later
pass. Unpublished keys with TTLs expire within one hour; pruning does not force
their removal. DeleteCollection removes the logical collection first and attempts
the same cleanup. The library runs no background cleanup workers.

Native indexes do not have TTLs. After acknowledged publication their predecessors
are dropped best-effort, without deleting documents. Uncertain publication never
drops the possibly current index. Call `store.PruneNativeIndexes(ctx, name)` in
database zero to reclaim obsolete indexes, including after deletion or failed
staging in either HASH or JSON storage. It checks each layout's own lease,
protects current revisions and active staging leases and returns an
index count, separately from hash/JSON pruning's key count. Concurrent changes may
leave work for a later pass. Native DeleteCollection also attempts index cleanup.

## Costs and deployment settings

The encoded collection limit defaults to 16 MiB, configurable with
`MaxCollectionBytes` up to 256 MiB. All layouts bound canonical snapshot size.
Hash publication additionally counts manifest bytes, lease/key names, hash field
names and values; JSON publication counts manifest, lease/key names and encoded
wrapper-document bytes (including escaped payload strings) against that limit. Projection overhead can therefore make a
format-1 collection too large to upgrade under the same configured limit. This is
an encoded byte budget, not Redis allocator overhead or peak process memory.
Reads/writes exceeding the configured limit return `ErrInvalidRecord`; they never
truncate. A client with a smaller limit may reject a collection another client
created. The size check occurs after downloading a read, so it does not bound the
network or allocation cost of an externally modified oversized Redis value.

Default and fallback reads transfer O(collection size) bytes; Query, filtered
Count and search additionally decode a memory collection. The exact selector
bounds candidates but the snapshot remains materialized. Native reads still
transfer the O(record count) token manifest; eligible Count avoids record payloads,
filtered Query transfers matching payloads, HNSW transfers candidate payloads,
and FLAT transfers all matching vector-bearing payloads. Unfiltered Query and Get/GetMany retain the whole-snapshot path. See
[local read measurements](performance.md#redis-native-read-measurements).

Every hash/JSON write stages a full generation; native mode also rebuilds its entire
index before publishing. Peak Redis storage includes old and staged data/graphs
plus obsolete generations/indexes pending cleanup. Index allocator overhead is
outside the encoded byte budget. Concurrent writers can multiply this work through
conflicts. This design favors read-heavy, bounded collections; no production
throughput or write-scalability claim is made.
Reserve the provider's internal key prefix for this library; do not edit values or
apply TTLs to collection keys.

For hash and JSON layouts, all generation keys and the manifest must survive together.
Use `noeviction`; independent eviction or external edits can leave dangling
references, which fail reads rather than returning partial records. Plain storage
needs no Search permissions/module. Hash storage uses HSET/HMGET, PEXPIRE, PERSIST,
PTTL, UNLINK and scripts. JSON storage uses JSON.TYPE (availability probe),
JSON.SET and JSON.GET alongside the same lease/publication/cleanup commands; no
Search or COMMAND permission is needed for plain JSON. Native HASH/JSON modes additionally need FT.CREATE, FT.INFO,
FT.SEARCH and FT.DROPINDEX; explicit index pruning needs FT._LIST. The provider does
not change global Search configuration.

Durability, replication and failover depend on Redis configuration. The local
Compose service enables AOF with `appendfsync everysec`, a persistent volume and
`noeviction`; it can still lose recent acknowledged writes on a crash. Configure
and verify backups/durability for the deployment. This provider does not configure
server persistence or provide failover acknowledgments. See Redis
[persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/).

Use `Username`, `Password` and a verified `TLSConfig` as appropriate. TLS settings
are cloned. Metadata reports system `redis`, configured database number and
namespace, excluding credentials and address. Context deadlines bound client
socket operations; cancellation is checked during preparation, conflict waits and
iteration. In-flight I/O without a deadline can take up to the five-second socket
timeout to return. Preserve errors with `errors.Is`/`errors.As`.

## Verification

```sh
go test -race ./stores/redis
go test -race -tags=integration -timeout=5m ./stores/redis
REDIS_TEST_IMAGE=redis:7.4 go test -count=1 -race -tags=integration -timeout=5m ./stores/redis
```

Normal tests need no server. Integration tests start a disposable `redis:8.2`
container by default; `REDIS_TEST_IMAGE` selects another image and `REDIS_TEST_ADDR`
can point to an existing disposable standalone server. Each factory uses its own
namespace and deletes only its collections. CI exercises Redis 7.4/8.2 on minimum
and current Go, separating storage and native index suites with five/ten-minute
timeouts respectively. It runs samples twice per supported layout, plus HNSW/FLAT/full-text
and explicit JSON conversion plus JSON-native HNSW/FLAT modes on 8.2. Tests cover all three layouts with
the shared model, embedding
and filter-parity contracts plus concurrent writes, integer sequence limits,
conflicts, uncertain replies, deadlines, corruption, size limits and client reopen.
Hash checks also cover a native publication error after PERSIST, failed/expiring
staging, retirement during reads, orphan pruning, explicit fixture upgrades and
Redis Search projection compatibility. Native suites run the shared contracts,
scalar differential cases, HNSW recall, FLAT exact-reference/roundoff cases,
tie fallback, physical algorithm drift, readiness
deadlines, concurrent publication, explicit rebuilds and uncertain-reply cleanup.
TEXT checks cover real tokenization/stemming, Unicode, NULL/empty/NUL values,
independent TAG equality, updates/deletes, visible schema drift, 128 fields,
legacy index versions, storage upgrades and generated sequences. Shared HNSW/FLAT
lifecycle fixtures include scalar/vector/TEXT fields on both HASH and JSON.
JSON-native checks additionally cover internal paths, full-precision canonical
results, projection byte budgets, unknown versions and publication conflicts.
Scalar checks additionally cover all key types, exact integer ranges, NULL truth,
physical path/separator drift, native routing, Count without payload decoding and
explicit upgrades from version-3 vector-only indexes. Hybrid checks cover RRF
scores/limits, both-list filters, all key types, optional vectors, HNSW recall,
roundoff/ties, generation retirement, embedding snapshots and missing TEXT indexes. JSON TEXT checks also cover
version-4 scalar-only compatibility, safe text paths, projection fidelity, storage
conversion and explicit rebuild without implicit read upgrades.
`REDIS_REQUIRE_SEARCH=true` requires the native tests and projection proof instead
of permitting skips on servers without Search. `REDIS_REQUIRE_JSON=true` similarly
requires the real RedisJSON contracts. JSON tests also check extreme numeric
precision, nonzero databases, module absence, partial staging, lost replies,
errors after PERSIST, read retirement, concurrent conversion, generated sequences,
corruption, pruning and compatibility with existing snapshot/hash fixtures.
