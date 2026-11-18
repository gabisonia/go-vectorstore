# Typed semantic search

Uses `EnsureTypedModelCollection`, automatic batched embeddings, native category
filters, and `SearchText`. The tagged [Article model](../internal/demo/demo.go)
and [main.go](main.go) show the complete flow. The default embedder is a tiny
keyword fixture for learning the API; it does not provide semantic understanding.

Run from the repository root with Go 1.25+:

```sh
go run ./samples/typed-search -category architecture
```

Expected fixture output:

```text
Matches: 1
costs: Reducing cloud costs (score 0.997, distance 0.003)
```

Use a persistent SQLite file or PostgreSQL:

```sh
go run ./samples/typed-search -sqlite /tmp/typed-search.db
aspire start --apphost samples/aspire-services/apphost.go
aspire wait postgres --apphost samples/aspire-services/apphost.go
go run ./samples/typed-search -dsn 'postgres://postgres:postgres@localhost:54329/vectorstore_test?sslmode=disable'
```

For real embeddings, export `OPENAI_API_KEY` and opt in:

```sh
go run ./samples/typed-search -openai -collection typed_search_openai -q 'reduce cloud costs'
```

`-openai` makes remote requests and can incur API charges. It defaults to
`text-embedding-3-small` at 1,536 dimensions. `OPENAI_EMBEDDING_MODEL`,
`OPENAI_BASE_URL`, and `-dimensions` configure another compatible text-embedding-3
model/endpoint. The adapter sends `dimensions` and requests float encoding; an
endpoint must support that contract. Inputs are batched two at a time, with
responses restored to input order using their indexes.

Flags: `-q`, `-category`, `-collection`, `-sqlite`, `-dsn`, `-openai`, `-dimensions`.
Memory is the default; `-dsn` and `-sqlite` are mutually exclusive. Fixture mode
always uses three dimensions. Search omits vectors from returned records.

The sample upserts three stable IDs in its dedicated collection. Memory disappears
on exit; SQLite/PostgreSQL records remain and reruns update those IDs. Use a
separate collection for each embedding model/mode/dimension: matching dimensions
do not make different embedding models compatible. The sample does not detect a
change of embedding model identity. To clean up, remove the closed sample SQLite
file (including any WAL/SHM sidecars), or explicitly delete just the dedicated
PostgreSQL collection through `DeleteCollection`. Stop the local services with
`aspire stop --apphost samples/aspire-services/apphost.go --force`; data volumes
remain. See [shared services](../aspire-services/README.md) for prerequisites.

[Tests](main_test.go) run memory and SQLite workflows, filtering, reruns and
cancellation without credentials. The adapter and RAG HTTP path are tested with
local stand-ins; these tests do not establish live-provider availability or quality.
