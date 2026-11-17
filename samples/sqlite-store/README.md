# Durable embedded collections

```sh
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db
```

The sample upserts two stable IDs, queries their native array data, searches only
the record with a vector, then closes/reopens the database to retrieve the draft
with its vector still absent. It needs no server, C toolchain, or API key. Reruns
update the same records; the file and collection remain for inspection.

See [SQLite storage and limits](../../docs/sqlite.md) and the
[model API](../../docs/model-api.md). The supplied vectors are instructional.

Run indexed scalar filtering in a separate `indexed_documents` collection:

```sh
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db -native
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db -native
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db -rebuild-indexes
```

The indexed model marks Title with IsIndexed and opens SQLite with NativeIndexes.
It prints `Indexed titles: 1` before the ordinary array/vector examples. Reruns
upsert the same records; `-rebuild-indexes` explicitly exercises transactional
backfill and implies `-native`. The default `documents` model is unchanged.
New collections build automatically; existing collections with the same registered
index hints need explicit backfill before indexed reads. Array filters/vector
search keep their existing scan behavior. No extension or C toolchain is needed.

Enable packed dense vectors or explicitly backfill an existing collection:

```sh
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db -vector-projections
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db -rebuild-vectors
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db -rebuild-vectors
go run ./samples/sqlite-store -db /tmp/vectorstore-demo.db -native -rebuild-vectors
```

`-rebuild-vectors` implies `-vector-projections`; both flags keep the registered
model unchanged and can be combined with scalar indexes. The sample additionally
runs an unfiltered dense search and prints `Unfiltered dense matches: 1`. New
collections build projections automatically. Existing or dirty collections use
canonical scanning until explicit backfill. The earlier array-filtered search
always uses the canonical path in this increment. Ranking stays exact in Go.
