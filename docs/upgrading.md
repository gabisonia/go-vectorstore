# Upgrading model collections

## Upgrading to v0.6.0

Use Go 1.26 or later for the root module. Applications on Go 1.25 can stay
on v0.5.0 until their toolchain is upgraded. Existing model and Record APIs
remain supported. The new native connectors attach to externally managed
schemas. Review the [provider coverage](parity.md) before selecting one.
Check [GitHub releases](https://github.com/gabisonia/go-vectorstore/releases)
for tag availability before updating the application module.

```sh
go get github.com/gabisonia/go-vectorstore@v0.6.0
```

## Upgrading to v0.5.0

Redis, bounded Qdrant, optional sqlite-vec, generated typed references and the new
embedding/input workflows are available in v0.5.0. Isolated sample modules pin
their own versions; inspect their go.mod when upgrading an example. Review
[API and storage compatibility](release-0.5.md) before upgrading applications.

`HybridSearchOptions` now contains a `KeywordTerms` slice and is no longer Go
comparable. Replace `==`/`!=` comparisons and uses as a map key with an
application-specific comparison or key. Use keyed literals for exported option
and schema structs, which have gained fields. Required core provider interfaces
and existing method signatures remain intact. The JSON and UUID behavior fixes
below can expose invalid application data/bindings previously accepted.

## v0.4.1 hardening update

Published v0.4.1 contains the production-hardening update.
The patch preserves public APIs and SQLite format 1, adds cancellation/recovery
checks, optimizes memory/SQLite exact search and SQLite reads, and expands samples
and AI documentation. Upgrade to the latest published 0.4.x patch, pin it, and
validate your workload. PostgreSQL iterators now report cancellation instead of
continuing to yield buffered rows, including cancellation during the last item if
the consumer continues iteration. See [release policy](releases.md) and
[production operation](production.md).

## Existing Record API applications

VectorStore, Collection, Record, TypedCollection and custom Codec users can
continue without converting their collections. Model collections are an opt-in
API with their own schema registration and native fields. Do not point a new
model definition at an existing Record table and expect it to convert the schema.
Create a separate collection, transform/upsert application records, validate
counts and search results, then switch the application when ready.

Building this checkout requires Go 1.26 or later. Applications on Go 1.25 can
keep using the published v0.5.0 release until they upgrade their toolchain.
PostgreSQL and SQLite dependencies are pinned in go.mod. Applications that
only use PostgreSQL do not import/link the SQLite driver unless they import that
provider or a sample that uses it.

PostgreSQL JSON/metadata numbers are decoded as json.Number, including nested
objects and arrays, preserving values above 2^53. Replace float64 assertions with
json.Number.Int64/Float64 as appropriate; Float64 can lose precision. PostgreSQL
required data, native vector and collection validation errors remain discoverable
through errors.Is/As. See the [model guide](model-api.md) for supported types.

## Adopting model collections

1. Define a native key, data properties and vector properties with struct tags or
   an explicit CollectionDefinition. Existing untargeted []int64/[]float64/[]bool
   fields keep their JSON mapping; native arrays require explicit type tags.
   UUID, bytes, calendar values and decimals are likewise opt-in types.
2. Choose a provider by the [capability matrix](parity.md). Reuse the definition
   across providers, checking optional indexes, hybrid and migration interfaces.
3. Configure runtime embedders at store, collection, or property level. Reconfigure
   these after reopening a store; registrations never contain embedder instances.
4. Ensure a new collection, map/insert records, and compare retrieval/filter/search
   results using realistic inputs. Explicit vectors must match dimensions/kind/
   precision; cosine requires nonzero values. Index limits can be tighter than
   storage limits, particularly sparse HNSW NNZ.

## Existing model collections

In v0.5.0, typed fields mapped to JSON retain their Go
representation during encoding. Earlier writes could quote `json.Number`, widen
float32 before JSON serialization, or bypass scalar marshaler methods. New writes
honor those representations and reject invalid numbers/marshaler failures. Native
scalar mappings, nil pointer SQL NULL behavior, registrations and storage formats
are unchanged. Existing records are read as stored; if affected values were
written previously, validate and re-upsert them from authoritative application
inputs. The provider does not rewrite stored JSON automatically.

JSON-mapped fields with a custom `UnmarshalJSON` now invoke it before native text
conversion or direct value assignment. Earlier reads could bypass that method for
strings, assignable maps/slices and other scalar values. Data rejected by your JSON
decoder now returns a field-labeled codec error with the original cause. Validate
previously stored values against your decoder, including records affected by the
JSON encoding issue above. Native mappings, SQL NULL pointer behavior and stored
registrations remain unchanged; the provider does not rewrite data on read.

Native arrays and native boolean scalars now consistently use native conversions
rather than falling through JSON decoding. Custom `UnmarshalJSON` methods on
native slice/element types and boolean scalars are no longer invoked by those
mappings. Use an explicit JSON mapping when JSON decoder behavior is intended.
Native arrays return independent slices, preserve SQL NULL/empty distinctions and
reject integer/float narrowing errors. Stored formats and existing data are unchanged.

In v0.5.0, non-string UUID key bindings require both value text
formatting (`encoding.TextMarshaler` or `fmt.Stringer`) and pointer text decoding
(`encoding.TextUnmarshaler`). This applies to model key fields and the typed key
parameter `K`, including generated UUIDs. Older constructors could accept a
decode-only type, return a generated key after committing a write, then reject
that key in subsequent Get/Delete/filter calls. Setup now returns
`ErrSchemaMismatch` before collection creation. Add the missing formatter or use
string/named-string bindings. Existing definitions, UUID storage and supported
bidirectional mappings remain compatible; this fix requires no data migration.

Required vectors remain the default. The new Optional flag is omitted from
serialized registrations when false, preserving previous required-vector
registrations. Binary/sparse vector absence uses missing map keys; typed nullable
fields use pointers, or an optional dense slice. See [optional vectors](model-api.md#optional-vectors)
for source embedding and upsert-clearing semantics.

Changing an existing required vector to optional is a schema change. EnsureExists
validates definitions and never applies that change automatically. On PostgreSQL,
use PlanMigration and review the generated nullability/storage steps and registry
finalization; nullability changes require a separate migration procedure. Adding
a **new optional vector**, including an index, fits the explicit additive
ApplyMigration subset and leaves existing rows NULL. Adding a required vector
needs a backfill and possibly re-embedding. Regenerate plans with the installed
provider version and apply only an unedited reviewed plan.

SQLite and memory reject mismatched definitions and do not apply migrations. For
SQLite schema changes, create a separately named collection and copy transformed
records in bounded batches before switching application configuration. Changing
providers uses the same logical types but does not copy storage automatically.
SQLite stores records in an internal format; it does not reuse PostgreSQL tables.

Queries and Count include records with missing optional vectors. Vector and
hybrid searches exclude them on the selected property before ranking and paging.
If an application expects keyword-only hybrid hits with no vector, use a separate
filtered text workflow or populate the missing vector first.

ContainsAny/ContainsAll are new optional conveniences over Contains compositions.
Review SQL NULL versus JSON null and empty operand behavior in the
[array filter guide](model-api.md#array-set-filters), especially under Not.

## Release validation

The release workflow runs the CI matrix before publishing: Go 1.26/current stable
race tests, vet, release-tool tests, PostgreSQL 16/18, Redis 7.4/8.2 and standalone
Qdrant integration tests, SQLite/optional-adapter checks, API compatibility, sample
checks and vulnerability scanning. Local equivalents are in the repository
workflow and [performance guide](performance.md). Run the model sample for each
provider you use and the persistent SQLite sample when adopting embedded storage.

The [v0.5.0 release workflow](https://github.com/gabisonia/go-vectorstore/actions/runs/44713607599)
passed all 25 jobs and published tag `v0.5.0` at commit
`50a9b02ad24682888585ed3d1bf37af6d463cbd9`. Public Go proxy/checksum verification and
a separate Go 1.25 consumer exercised single-record APIs, exact search and SQLite
reopen behavior. For future releases, follow the
[release instructions](../README.md#release-automation) with a new version; published
tags stay on their original commits.
