# SQLite model provider

`stores/sqlite` provides durable embedded model collections backed by
[modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite). It needs no database
server, extension, C compiler, or API key.

```go
store, err := sqlite.Open(ctx, "/var/lib/myapp/vectors.db", sqlite.StoreOptions{})
if err != nil {
    return err
}
defer store.Close()
// store implements vectordata.ModelStore and CapabilityProvider.
collection, err := store.EnsureModelCollection(ctx, "documents", definition,
    vectordata.ModelCollectionOptions{})
```

Use the same CollectionDefinition, struct codecs, typed wrappers, embedding
defaults, filters, and model operations as memory/PostgreSQL. All 20 native data
types, native keys, generated int64 keys, multiple named vectors, nullable vectors,
float16 rounding, and dense/binary/sparse exact search are supported. Binary and
sparse search use their optional interfaces. Vector projections default to off.

Paths are literal filenames, including punctuation; driver DSNs and custom PRAGMAs
are not accepted. Native text values, keys, and model names must be valid UTF-8;
invalid byte sequences are rejected rather than replaced during JSON encoding.
The parent directory must exist. `:memory:` opens a private
transient database for that Store. Collection names are bound parameters. Runtime
embedders are never serialized; configure them again when reopening a store.

## Durability and concurrency

Open configures WAL mode, FULL synchronization and foreign keys on every
connection. Lock acquisition and retryable reads wait in Go with a five-second
limit, so caller cancellation/deadlines can stop the wait. File durability follows
SQLite/filesystem semantics; the `:memory:` variant is transient. See SQLite's
[WAL documentation](https://www.sqlite.org/wal.html) and
[synchronous settings](https://www.sqlite.org/pragma.html#pragma_synchronous).
Each Store owns one pooled connection; multiple handles/processes can access the
same local database file. SQLite immediate write transactions serialize batches
and generated-key allocation. Competing writers wait up to five seconds or the
caller's context limit, whichever arrives first. Cancellation/deadlines remain
discoverable with `errors.Is(err, context.Canceled)` or
`errors.Is(err, context.DeadlineExceeded)`. Reaching the five-second
contention limit returns the underlying SQLite busy error. Retries acquire a
transaction or repeat a side-effect-free read; write statements and commits are
not replayed.

Insert, Upsert, Delete, collection creation/deletion, and generated sequences are
transactional. Failed record batches roll back every row and sequence update.
Explicit keys do not advance the generated sequence: a collision fails without
overwriting an existing record. SQL NULL and JSON null are stored distinctly;
JSON numbers and exact decimals retain precision. Native timestamps and arrays
are reconstructed according to the registered schema.

A collection registration must exactly match the normalized definition; a stale
handle returns ErrSchemaMismatch on reads/writes. Storage format 1 is internal to
this provider. Open creates missing provider tables, while EnsureExists creates or
validates a collection. Neither operation migrates an existing definition.

Close the Store before removing/replacing its file. For backup of a live database,
use SQLite-compatible backup tooling that includes WAL state. A copied main file
alone may omit committed WAL data. Use SQLite on a local filesystem that supports
its locking semantics; see the upstream [WAL restrictions](https://www.sqlite.org/wal.html).

## Query costs and provider limits

Get fetches one record directly. GetMany normalizes/deduplicates native keys and
uses the collection/key primary index in chunks of at most 500 keys. Metadata and
all chunks use one deferred read transaction, so results stay on the same WAL
snapshot while other connections can write. Only matching payloads are decoded
and validated; missing keys are omitted, duplicates collapse, and ordering is
unspecified. The connection is released before results reach the caller.

Count with a nil filter uses SQL COUNT over the collection's record index, with
schema/format validation in the same statement. It does not deserialize payloads
and uses bounded Go memory; counting still scans the matching index entries and
can grow with collection size. These fast paths do not invoke embedders.

Query, filtered Count, and all search operations load a consistent snapshot of the
entire collection into memory, release the SQL connection, and use the shared
exact-search engine. Snapshot replay never invokes an embedder. Iterators
materialize that snapshot before yielding and hold no open SQL cursor while
application code runs. These operations cost O(collection size) memory and scan
work; vector search also evaluates distance for each candidate. Benchmark
realistic sizes before selecting the provider for large collections or concurrent
query services. See the [read benchmarks](performance.md#sqlite-batch-lookup-and-count-verification).

Index flags are accepted as portability hints; no physical property/vector
indexes, ANN acceleration, full-text search, hybrid search, PostgreSQL tuning,
or migration planning/application are advertised. Unknown ProviderOptions return
ErrUnsupported. Use PostgreSQL when those capabilities are needed.

Examples:

```sh
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db
go run ./samples/model-store -sqlite /tmp/model-store.db
```

The first retains records across reopen; the second demonstrates store-wide
embedding defaults and deletes only the records generated by that run. Tests run
the shared conformance suite plus file reopen, batch rollback, concurrent handles,
context cancellation, and iterator cleanup in normal `go test ./...` CI.
