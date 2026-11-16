# Focused model recipes

Run from the repository root with Go 1.25+. [main.go](main.go) contains each
workflow in a separate switch case, sharing sample-only provider setup. These
recipes use supplied vectors or a deterministic instructional embedder, so they
need no API key.

| Recipe | Command | Expected result |
| --- | --- | --- |
| Named vectors and projection | `go run ./samples/recipes -recipe named-vectors` | `guide: BodyVector distance 0.000; loaded dimensions 3` |
| Squared Euclidean distance | `go run ./samples/recipes -recipe squared-l2` | `near: distance 25 score 0.038462`, then `Squared-distance threshold 5: 1 match` |
| Early iterator exit and cancellation | `go run ./samples/recipes -recipe iterators` | `First: one`, then cancellation and a count of 2 |
| Text/custom-input read iterators | `go run ./samples/recipes -recipe embedded-iterators` | `Search: costs`, then `Input: costs` |
| Provider capabilities | `go run ./samples/recipes -recipe capabilities` | Backend name, hybrid/migration flags and physical index tuple count |
| Store/collection metadata | `go run ./samples/recipes -recipe metadata` | `System: memory` and `Collection: recipe_demo_metadata` |
| PostgreSQL hybrid search | `go run ./samples/recipes -recipe hybrid -dsn "$PG_DSN"` | Ranked IDs including `costs`, with `reciprocal_rank_fusion` scores |
| PostgreSQL migration preview | `go run ./samples/recipes -recipe migration -dsn "$PG_DSN"` | Proposed optional Category column SQL/args and `Preview only; no migration applied.` |

For the last two recipes, start the root compose service and set the DSN:

```sh
docker compose up -d postgres
export PG_DSN='postgres://postgres:postgres@localhost:54329/vectorstore_test?sslmode=disable'
go run ./samples/recipes -recipe hybrid -dsn "$PG_DSN"
go run ./samples/recipes -recipe migration -dsn "$PG_DSN"
```

The metadata recipe requires master after v0.4.1. It obtains a typed handle and
prints system, database, namespace and collection identity without creating a
collection. SQLite reports the filename and PostgreSQL the configured database
and schema. Empty fields mean unavailable. This is local configuration discovery;
it does not probe server health or collection existence.

All recipes accept `-sqlite /tmp/recipes.db` or `-dsn` (mutually exclusive), and
`-prefix` to change the default `recipe_demo` collection prefix. Capabilities is a
local metadata query; it is not a server health/version/permission probe. Hybrid
and migration recipes return an unsupported error on memory/SQLite before creating
any collection. Accepted index hints there do not create physical vector indexes.

The iterator recipe checks both yielded errors and the consumer context: a SQL
driver can have buffered rows before cancellation. Early exit closes the cursor
before the follow-up count.

Hybrid search uses the full-text `Body` property and RRF. Its score is a fusion
score, not the similarity score from ordinary vector search.

Migration creates/reuses its own baseline fixture collection, then calls only
`PlanMigration` to inspect an optional data addition. Planning is read-only; the
recipe's initial fixture creation writes schema. It never calls `ApplyMigration`
or executes the plan's SQL. SQL and arguments stay separate, risk flags are
printed, and finalization is shown separately. For an existing application model,
plan against that model's registered definition and review the full report using
the [migration guide](../../docs/migrations.md).

Named-vector, squared-distance and iterator fixtures upsert stable IDs. Records remain in persistent
providers; reruns reuse their collections. Capabilities and metadata create none. To clean up,
remove a closed sample SQLite file and its sidecars, or delete only collections
under your chosen prefix through `DeleteCollection`. Stop local PostgreSQL with
`docker compose down`.

The squared-distance recipe requires master after v0.4.1. Its thresholds use
squared units; ordinary L2 distance 5 becomes squared distance 25. See the
[metric contract](../../docs/model-api.md#squared-euclidean-distance).

The iterator recipe also lists collections and batch-reads a found/missing key.
The hybrid recipe enumerates `HybridSearchTextIter`. These iterator APIs require
master after v0.4.1. PostgreSQL reads cursor rows; memory/SQLite buffer snapshots
or pages before yielding. A break closes the operation, and every enumeration
reruns its read and embedding work.

Portable workflows are exercised by [main_test.go](main_test.go). PostgreSQL
16/18 integration runs every recipe twice and checks that previewing a migration
leaves the proposed column absent:

```sh
go test -race -tags=integration -timeout=3m ./samples/recipes
```

Docker is required unless `PGVECTOR_TEST_DSN` points to a disposable database;
`PGVECTOR_TEST_IMAGE` selects the pgvector image. Each test uses a private schema
and removes it afterward.
