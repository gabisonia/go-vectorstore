# Opt-in SQLite scalar L1 execution

```sh
go run ./samples/sqlitevec-store -db /tmp/sqlitevec-demo.db
go run ./samples/sqlitevec-store -db /tmp/sqlitevec-demo.db
go run ./samples/sqlitevec-store -db /tmp/sqlitevec-demo.db -rebuild-vectors
```

The sample imports `stores/sqlitevec`, links the pure-Go extension and uses scalar
L1 candidate selection with exact Go ranking. It upserts three stable IDs, excludes
the draft without a vector, and reopens the file for another search. It prints:

```text
near: L1 distance 3
far: L1 distance 12
After reopen: near (dimensions 2)
```

No server, C compiler or API key is needed. Reruns retain the file and update the
same records. New collections build packed projections atomically; an existing or
dirty collection requires `-rebuild-vectors` for native execution and otherwise
uses canonical scanning. Other metrics and filtered searches keep their existing
paths. This sample makes no vector-index or throughput claim. See
[sqlite-vec execution and precision](../../docs/sqlitevec.md).
