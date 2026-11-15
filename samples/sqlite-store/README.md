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
