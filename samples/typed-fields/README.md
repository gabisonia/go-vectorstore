# Generated typed filters and ordering

Run from the repository root with Go 1.25+; no credentials are needed:

```sh
go generate ./samples/typed-fields
go run ./samples/typed-fields
go run ./samples/typed-fields -sqlite /tmp/typed-fields-demo.db
go run ./samples/typed-fields -sqlite /tmp/typed-fields-demo.db
```

Expected output:

```text
Matches: 1
go: Go guide
Unrated: 1
```

The committed generated references follow `Article`'s tags and logical names.
The sample combines typed boolean/range/array filters with a dynamic JSON path,
orders results, counts NULL ratings and reuses the filter for exact vector search.
`Title` has a storage alias; `Tags` uses named `Labels`/`Label` types that retain
canonical string-array storage. Both fixed fixture IDs are upserted on each run;
the dedicated `typed_fields_demo` collection and SQLite file persist until you
remove them. Use a fresh file or a dedicated database for the sample.

`-dsn` selects PostgreSQL and is mutually exclusive with `-sqlite`. The shared
[Aspire services](../aspire-services/README.md) provide a local instance:

```sh
go run ./samples/typed-fields -dsn 'postgres://postgres:postgres@localhost:54329/vectorstore_test?sslmode=disable'
```

For a freshness check from this directory:

```sh
go run ../../cmd/vectordata-gen -type Article -check
```

Misspelled fields and wrong Go operand types fail compilation. Regenerate after
model changes and check freshness in CI, especially after tag edits. JSON paths,
value validity and backend support retain runtime validation; see the
[typed reference contract](../../docs/typed-fields.md).
