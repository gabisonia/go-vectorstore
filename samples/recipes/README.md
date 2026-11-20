# Focused model recipes

Run from the repository root with Go 1.26+. [main.go](main.go) contains each
workflow in a separate switch case, sharing sample-only provider setup. These
recipes use supplied vectors or a deterministic instructional embedder, so they
need no API key.

| Recipe | Command | Expected result |
| --- | --- | --- |
| Single-record CRUD and optional lookup | `go run ./samples/recipes -recipe single-record` | `Missing found: false`, `Deleted: true; repeated delete: false` |
| Named vectors and projection | `go run ./samples/recipes -recipe named-vectors` | `guide: BodyVector distance 0.000; loaded dimensions 3` |
| Key-filter query/count | `go run ./samples/recipes -recipe key-filters -sqlite /tmp/recipes.db` | `Matched keys: 2`, `Paged key: 2`, `Exact-key count: 1` |
| Scalar filtering and paging | `go run ./samples/recipes -recipe scalar-query -sqlite /tmp/recipes.db` | `Scalar count: 3`, `Scalar page: 3 rank 2` |
| Squared Euclidean distance | `go run ./samples/recipes -recipe squared-l2` | `near: distance 25 score 0.038462`, then `Squared-distance threshold 5: 1 match` |
| Early iterator exit and cancellation | `go run ./samples/recipes -recipe iterators` | `First: one`, then cancellation and a count of 2 |
| Text/custom-input read iterators | `go run ./samples/recipes -recipe embedded-iterators` | `Search: costs`, then `Input: costs` |
| Provider capabilities | `go run ./samples/recipes -recipe capabilities` | Backend name, hybrid/migration flags and physical index tuple count |
| Store/collection metadata | `go run ./samples/recipes -recipe metadata` | `System: memory` and `Collection: recipe_demo_metadata` |
| PostgreSQL hybrid search | `go run ./samples/recipes -recipe hybrid -dsn "$PG_DSN"` | Ranked IDs including `costs`, with `reciprocal_rank_fusion` scores |
| PostgreSQL migration preview | `go run ./samples/recipes -recipe migration -dsn "$PG_DSN"` | Proposed optional Category column SQL/args and `Preview only; no migration applied.` |

For the last two recipes, start the [Aspire services](../aspire-services/README.md)
and set the DSN:

```sh
aspire start --apphost samples/aspire-services/apphost.go
aspire wait postgres --apphost samples/aspire-services/apphost.go
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
The recipe runs both the original string query and a `KeywordTerms` list. The
list section prints `Keyword list: costs:` and uses OR between entries, with AND
between words inside `"cloud costs"`. It requires `HybridKeywordTerms` capability
and master after v0.4.1. Terms are plain text; the positional keyword string is
blank in list mode. See [keyword-list semantics](../../docs/model-api.md#keyword-lists).

Migration creates/reuses its own baseline fixture collection, then calls only
`PlanMigration` to inspect an optional data addition. Planning is read-only; the
recipe's initial fixture creation writes schema. It never calls `ApplyMigration`
or executes the plan's SQL. SQL and arguments stay separate, risk flags are
printed, and finalization is shown separately. For an existing application model,
plan against that model's registered definition and review the full report using
the [migration guide](../../docs/migrations.md).

Named-vector, scalar-query, key-filter, squared-distance and iterator fixtures upsert stable IDs. Records remain in persistent
providers; reruns reuse their collections. Capabilities and metadata create none. To clean up,
remove a closed sample SQLite file and its sidecars, or delete only collections
under your chosen prefix through `DeleteCollection`. Stop local PostgreSQL with
`aspire stop --apphost samples/aspire-services/apphost.go --force`; volumes remain.

The squared-distance recipe requires master after v0.4.1. Its thresholds use
squared units; ordinary L2 distance 5 becomes squared distance 25. See the
[metric contract](../../docs/model-api.md#squared-euclidean-distance).

The `key-filters` recipe counts a key `In` set with duplicate, missing and NULL
operands, then sorts the matching records by Rank and returns a page. It also
counts one exact key. The recipe is available on master after v0.4.1. SQLite's
key fast paths use its existing index, with Go sorting/paging over matching rows;
the same public calls work on memory and PostgreSQL. See [SQLite query costs](../../docs/sqlite.md#query-costs-and-provider-limits).

The `scalar-query` recipe combines string equality and an integer range, counts
the matches, then orders by Rank and skips the first match before returning one
record. It works on all three providers. On master after v0.4.1, SQLite executes
this filter and page in SQL and decodes only the page; it still scans payload JSON
without a property index. Unsupported filter/sort combinations keep snapshot
evaluation. See [scalar measurements](../../docs/performance.md#sqlite-scalar-query-verification).

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

The `single-record` recipe demonstrates typed UpsertOne/TryGet/DeleteOne and dynamic
InsertOne/TryGet/DeleteOne. It deletes its two records, so repeated runs are safe:

```sh
go run ./samples/recipes -recipe single-record -sqlite /tmp/recipes.db
go run ./samples/recipes -recipe single-record -sqlite /tmp/recipes.db
```

The collection remains. A missing key returns `found=false, err=nil`; Get still
returns ErrNotFound. Provider, codec and collection errors remain errors. Prefer
batch writes when processing multiple records to combine embedding requests and
storage work. Explicit/generated keys follow the same rules as existing batches.
