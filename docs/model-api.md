# Model-driven vector stores

The model API follows the main ideas of `Microsoft.Extensions.VectorData`:
application models describe key, data and vector properties; providers use that
same definition for mapping, storage, indexes and search. The original `Record`,
`Collection`, `VectorStore`, and `TypedCollection[T]` APIs remain supported.

## A typed collection

```go
type Product struct {
    ID        string    `vectordata:"key,name=product_id"`
    Name      string    `vectordata:"data,index"`
    Tags      []string  `vectordata:"data,index"`
    Embedding []float32 `vectordata:"vector,dimensions=3,index"`
}

store := memory.New() // Or an opened SQLite store or postgres.PostgresVectorStore.
products, err := vectordata.EnsureTypedModelCollection[string, Product](
    ctx, store, "products", nil, vectordata.ModelCollectionOptions{},
)
if err != nil {
    return err
}
_, err = products.Upsert(ctx, []Product{{
    ID: "p1", Name: "Quiet keyboard", Tags: []string{"office"},
    Embedding: []float32{1, 0, 0},
}})
if err != nil {
    return err
}
results, err := products.Search(ctx, []float32{1, 0, 0}, 5,
    vectordata.ModelSearchOptions{
        Filter: vectordata.Contains(vectordata.Column("Tags"), "office"),
    })
```

Import `vectordata` and `stores/memory` from this module. See
[`samples/model-store/main.go`](../samples/model-store/main.go) for a complete
program that runs without an API key and can switch to PostgreSQL with `-dsn`.
Its deterministic three-dimensional embedder illustrates the API; use a real
embedding model for semantic search.

`GetTypedModelCollection[K,T]` constructs a handle without database access.
`EnsureTypedModelCollection[K,T]` additionally creates or validates storage.
These are generic functions because Go does not support methods with their own
type parameters. Both struct values and pointers to structs are supported.
Reflection mappings are resolved once when constructing a `StructCodec[T]`.
Custom `ModelCodec[T]` implementations can provide their own mappings.

## Schema definitions and tags

The equivalent explicit `CollectionDefinition` has one `KeyProperty`, a list of
`DataProperty` values, and zero or more `VectorProperty` values. Pass a pointer to
an explicit definition to the typed factory, or pass nil to infer tags.
Definitions are validated and defensively copied. Logical names correspond to Go
field names for struct mapping; `StorageName` controls the database column name.
Logical names must not have surrounding whitespace. Empty/NUL names, duplicate
names, names over 63 bytes, and the reserved `__vs_` prefix are rejected.

| Tag role | Options |
| --- | --- |
| `key` | `name=<storage name>`, `type=string\|int64\|uint64\|uuid`, `generated` |
| `data` | `name=<storage name>`, `type=<data type>`, `optional`, `index`, `fulltext` |
| `vector` | `name=<storage name>`, `dimensions=<n>`, `kind=dense\|binary\|sparse`, `precision=float32\|float16`, `metric=cosine\|l2\|inner_product\|l1\|hamming`, `index`, `method=hnsw\|ivfflat`, `source=<data field>`, `optional`, `input` |
| `-` | Ignore the field |

Untagged fields are ignored when inferring a definition. Exported fields and
exported embedded fields are supported. Invalid tags and type conflicts fail
before storage creation. An explicit definition uses exported Go field names and
does not require tags.

Supported native key types are string, int64, uint64 and UUID. UUID keys can be
strings or compatible types with text mapping. PostgreSQL stores these as `text`,
`bigint`, `numeric(20,0)` and `uuid`, rather than stringifying all keys into a text
column. Unsigned fields infer `KeyUint64`; typed wrappers use `uint64` (including
defined types) or a 64-bit `uint`. Values from 0 to 18446744073709551615 retain
their exact identity across writes, reads, filters, ordering and deletes. Native
integer arguments are accepted when in range; floats, numeric strings and negative
values are rejected for uint64 keys. SQLite decodes unsigned JSON keys directly
into uint64, preserving values above 2^53 and MaxInt64 after reopening a file.

`key,generated` supports int64-compatible fields and UUID fields. PostgreSQL uses
identity columns for int64 keys. All providers generate random version 4 UUIDs
in the application, then insert them as explicit values; no database UUID default
or additional extension is required. Unsigned and arbitrary string generation
are unsupported. Use `type=uuid` explicitly for string UUID fields.

A zero generated key field in a tagged model (or a nil `ModelRecord.Key`) requests
a new key. An explicit dynamic key, including an all-zero UUID string, is preserved.
Insert/Upsert return keys in input order; input models are not mutated. Assign the
returned key before upserting the same model again. A generated-key collision
fails instead of overwriting a record, including during Upsert. Failed memory and
SQLite batches preserve integer sequence state; PostgreSQL identity values can
have gaps after a rollback. See the runnable
[native-key sample](../samples/native-keys/README.md).

Data types are `string`, `int64`, `float64`, `bool`, `time`, `string_array`, `json`,
`uuid`, `bytes`, `int64_array`, `float64_array`, `bool_array`, `uuid_array`,
`time_array`, `date_only`, `time_only`, `date_only_array`, `time_only_array`,
`decimal`, and `decimal_array`.
Pointer data fields
are optional. Nonoptional fields reject nil values.
JSON numbers retain their precision as `json.Number`. Dynamic records use logical
property names in `ModelRecord.Data` and `ModelRecord.Vectors`.

PostgreSQL uses native columns, B-tree indexes for scalar `index` properties,
GIN indexes for indexed native arrays, GIN full-text indexes for `fulltext`
strings, and optional per-vector HNSW/IVFFlat indexes. JSON properties currently
require provider-specific indexes and reject the generic `index` flag.

UUID and byte data are opt-in, using tags or `DataProperty.Type`:

```go
type Document struct {
    ID      string `vectordata:"key"`
    Owner   string `vectordata:"data,type=uuid,index"`
    Backup  *string `vectordata:"data,type=uuid"`
    Digest  []byte `vectordata:"data,type=bytes,index"`
    Content []byte `vectordata:"data,type=bytes,optional"`
    Legacy  []byte `vectordata:"data"` // Existing JSON/base64 mapping.
}
```

`TypeUUID` maps to a PostgreSQL `uuid` column. Dynamic records return lowercase,
hyphenated UUID strings. Values use the same strict 36-character form as UUID keys;
string aliases and compatible `encoding.TextMarshaler`/`fmt.Stringer` values are
accepted. Typed UUID fields can be strings or types with value text formatting and
pointer `encoding.TextUnmarshaler` support. Invalid UUIDs fail before writing.

`TypeBytes` maps to `bytea`, accepts byte slices and slice aliases, and returns
`[]byte` in dynamic records. A non-nil empty slice is an empty binary value. A nil
slice is SQL NULL for an optional property and an error for a required property.
Providers copy buffers on writes and reads, so callers can reuse their buffers.

Native UUID/byte properties support equality, membership, existence, ordered
comparisons and query ordering, with the usual SQL null semantics. UUID comparisons
use canonical UUID order; bytes use unsigned lexicographic order, including prefix
length. `Contains` means native array membership, not byte substring matching.
The `index` flag creates a B-tree index for either native type. Memory applies the
same validation and comparison rules without physical indexes.

Type inference is unchanged: an ordinary string remains `string`, and byte slices
without `type=bytes` remain JSON values (base64 for `[]byte`). Existing models and
registrations do not silently change. Adding optional native fields is supported
by additive migrations; changing an existing JSON/string/native mapping requires
manual conversion and backfill review.

Numeric, boolean, UUID and timestamp arrays are also opt-in:

```go
type Measurements struct {
    ID     string    `vectordata:"key"`
    Codes  []int64   `vectordata:"data,type=int64_array,optional,index"`
    Values []float64 `vectordata:"data,type=float64_array,optional,index"`
    Flags  []bool    `vectordata:"data,type=bool_array,optional,index"`
    Owners []string  `vectordata:"data,type=uuid_array,optional,index"`
    Dates  []time.Time `vectordata:"data,type=time_array,optional,index"`
    Legacy []int64   `vectordata:"data"` // Retains JSON storage.
}
filter := vectordata.Contains(vectordata.Column("Codes"), int64(42))
_ = filter
```

`TypeInt64Array`, `TypeFloat64Array` and `TypeBoolArray` map to PostgreSQL
`bigint[]`, `double precision[]` and `boolean[]`. Dynamic reads return `[]int64`,
`[]float64` and `[]bool`. Typed integer fields can use integer slices, floating
fields can use float32/float64 slices, and boolean fields can use bool slices,
including named slice/element aliases. Integer bounds and floating-point
finiteness are checked; typed reads reject values outside the destination range.
Dynamic records may also supply `[]any` with valid non-null scalar elements.

`TypeUUIDArray` maps to `uuid[]` and returns canonical `[]string` values in dynamic
records. Its elements follow the same strict UUID validation and typed text mapping
as scalar UUID properties. Typed fields support strings, string aliases, and UUID
types with value text formatting and pointer text decoding.

`TypeTimeArray` maps to `timestamp with time zone[]` (`timestamptz[]`) and returns
`[]time.Time`. Typed fields accept `time.Time` or defined types convertible to it,
including named slice types. Elements normalize to UTC and truncate sub-microsecond
precision before storage and filtering, so timezone-equivalent instants compare
identically in all three providers. UTC years must be 1–9999; strings, infinities and
out-of-range instants are rejected. This normalization applies to timestamp arrays;
scalar `TypeTime` behavior is unchanged.

For these new types, nil slices are SQL NULL for optional fields and invalid for
required fields. Non-nil empty slices stay empty arrays. Elements cannot be NULL,
and floats cannot be NaN or infinite. Buffers are copied on writes and reads.
Existing string-array nil-slice behavior is unchanged. Unannotated numeric,
boolean and timestamp slices continue to use JSON; unannotated string slices
remain `string_array`. UUID arrays require an explicit type. Registrations do not
change implicitly.

`Contains` tests one scalar element with a GIN-compatible typed predicate. `Eq`
and `In` compare whole arrays, preserving order and duplicates. NULL arrays retain
SQL unknown behavior under negated membership; an empty array contains nothing.
A NULL membership operand matches no native element. Portable array ordering and
ordered array comparisons are unsupported. Arrays written directly through SQL
must be one-dimensional with lower bound 1 and non-null, finite elements;
unsupported shapes/values are rejected on read.

Adding optional native array fields and their requested GIN indexes is supported
by additive migrations. Required additions need backfill; changing JSON or an
existing native array type requires manual conversion review.

Date-only and time-only values are opt-in calendar properties, separate from
timestamp instants:

```go
type Appointment struct {
    ID       string                `vectordata:"key"`
    Day      vectordata.DateOnly    `vectordata:"data,type=date_only,index"`
    Starts   *vectordata.TimeOnly   `vectordata:"data,type=time_only,index"`
    Holidays []vectordata.DateOnly  `vectordata:"data,type=date_only_array,optional,index"`
    Slots    []string              `vectordata:"data,type=time_only_array,optional,index"`
}
day, err := vectordata.ParseDateOnly("2024-02-29")
_ = day
_ = err
clock, err := vectordata.ParseTimeOnly("09:02:03.123456789") // 09:02:03.123456
_ = clock
_ = err
```

`TypeDateOnly` maps to PostgreSQL `date`; `TypeTimeOnly` maps to
`time without time zone`. Their array counterparts map to `date[]` and
`time without time zone[]`. Dynamic records return canonical `string`/`[]string`
values. Typed fields accept strings and string aliases, including the provided
`DateOnly`/`TimeOnly` helpers, and slices of these types. Pointers make scalar
properties optional. The helpers provide text/JSON mapping and parsing; native
storage still requires an explicit tag or `DataProperty.Type`.

Dates require exactly `YYYY-MM-DD`, valid Gregorian days and years 1–9999.
Clock times require `HH:MM:SS` with an optional 1–9 digit fractional second and
normalize to `HH:MM:SS.ffffff` by truncating sub-microsecond precision. Clock
times range from midnight through `23:59:59.999999`; offsets, timezone suffixes,
leap seconds and `24:00:00` are rejected. Neither type accepts `time.Time` values
or implicitly extracts a date/time from a timestamp. Empty strings and zero-valued
helper strings are invalid calendar values; use nil pointers for optional NULLs.
PostgreSQL values outside these bounds, including date infinities, are rejected
on read. Native reads are independent of session `DateStyle` and `TimeZone`.

Scalar calendar fields support equality, membership, ordered comparisons and
sorting, with B-tree indexes when requested. Arrays support the native array
filters, NULL/empty distinction and GIN indexes described above. Invalid values
fail before writes. Existing string/string-array/JSON inference, timestamp
properties and registrations retain their existing behavior. Optional additions
are additive migrations; required fields need backfill, and conversions from
existing strings, timestamps or JSON require manual migration review.

Exact decimal properties and arrays are also opt-in:

```go
type Product struct {
    ID       string                 `vectordata:"key"`
    Price    vectordata.Decimal     `vectordata:"data,type=decimal,index"`
    Discount *vectordata.Decimal    `vectordata:"data,type=decimal"`
    History  []vectordata.Decimal   `vectordata:"data,type=decimal_array,optional,index"`
}
price, err := vectordata.ParseDecimal("9007199254740993.000000001")
_ = price
_ = err
```

`TypeDecimal` maps to unconstrained PostgreSQL `numeric`, avoiding database scale
rounding. `TypeDecimalArray` maps to `numeric[]`. Dynamic records return canonical
`Decimal`/`[]Decimal` values. Typed fields accept string types, including `Decimal`,
string aliases and `json.Number`, and their slices. Dynamic writes and filters
also accept signed/unsigned integers and integer slices; floating-point inputs
are rejected so binary floating-point conversion cannot silently change a price.

Values match the [.NET decimal value range](https://learn.microsoft.com/en-us/dotnet/api/system.decimal):
a coefficient from 0 through 79228162514264337593543950335, a separate sign, and
scale 0–28. Parsing accepts decimal text with an optional signed exponent,
canonicalizes redundant zeros/exponents and negative zero, and rejects any value
requiring rounding or outside these bounds. Display scale and trailing zeros are
not preserved. Use `"0"` for zero; the zero-valued helper string is invalid.
NaN, infinity, whitespace and rational fractions such as `1/3` are invalid.
PostgreSQL values outside the portable range are rejected on read.

`Decimal` supports text mapping and encodes as an exact JSON number. JSON decoding
accepts numbers, rejecting quoted strings and null; use pointers for optional
values. The helper does not implement decimal arithmetic. Storage, comparisons,
sorting and filters never convert through `float64`. Scalars use B-tree indexes;
array membership uses GIN, with the same NULL/empty and whole-array equality rules
as other native arrays. Decimal operands in JSON-path comparisons also retain
numeric semantics and exact precision. Existing string, float and JSON inference is unchanged.
Optional additions support additive migrations; required fields need backfill,
and conversions from existing floats, strings or JSON require manual review.

PostgreSQL model collections use strict schema validation: columns, types,
nullability, identity and primary-key shape must match. Existing indexes are
checked for conflicts. Definitions are registered in PostgreSQL's reserved
`__vectorstore_models` table. SQLite registers definitions in
`__vectorstore_collections` alongside internal record storage; memory keeps them
in process. Runtime embedder objects are not persisted. Memory record operations
reject handles whose normalized definition differs from the current collection,
including after deletion/recreation. Writes recheck the definition after embedding
and before committing records; stale handles fail with `ErrSchemaMismatch`. Changing a registered
model requires an explicit migration or a new collection. The original Record API's
strict/auto-migrate modes are unchanged. PostgreSQL also exposes a
[migration API](migrations.md) for read-only review and explicit, atomic application
of new optional data/vector properties and new indexes.

## Provider capabilities

Built-in stores implement the optional `vectordata.CapabilityProvider` interface.
`Capabilities()` returns a local `ProviderCapabilities` snapshot for the model
API. Applications can inspect supported keys, generated keys, data types,
distance metrics and physical indexes before choosing a model or search flow:

```go
// store is a vectordata.ModelStore. Import fmt and slices from the standard library.
if provider, ok := store.(vectordata.CapabilityProvider); ok {
    caps := provider.Capabilities()
    fmt.Printf("%s: UUID keys=%t, hybrid search=%t\n", caps.Backend,
        slices.Contains(caps.KeyTypes, vectordata.KeyUUID), caps.HybridSearch)
}
```

All three providers report string/int64/uint64/UUID keys, generated int64/UUID keys, all twenty
portable data types, and cosine/L2/inner-product/L1/Hamming metrics.
`VectorKinds` reports dense, binary and sparse support. Hamming applies only to
binary vectors; the other metrics apply to dense and sparse vectors.
`VectorPrecisions` reports `VectorFloat32` and `VectorFloat16` on all three providers;
PostgreSQL float16 storage requires pgvector 0.7+ and a visible `halfvec` type.

| Feature | Memory | SQLite | PostgreSQL |
| --- | --- | --- | --- |
| `InputEmbedding` | True, dense outputs | True, dense outputs | True, dense outputs |
| `OptionalVectors` | True | True | True |
| `VectorIndexMethods` | None | None | HNSW, IVFFlat |
| `VectorIndexCapabilities` | None | None | Supported method/metric/precision combinations and dimension/nonzero limits |
| `IndexedDataTypes` | None | None | String, int64, float64, bool, time, UUID, bytes, calendar values, decimals and native arrays |
| `FullTextIndexing` | False | False | True, for string properties |
| `HybridSearch` | False | False | True |
| `MigrationPlanning` | False | False | True, plan-only |
| `MigrationApplication` | False | False | True, additive changes only |

Index fields describe physical acceleration. Memory and SQLite accept index hints
for portability without creating physical property or vector indexes. Generic
JSON indexing remains unsupported; provider-specific indexes are outside this
metadata. Except for the detailed combination list below,
empty slices mean no support for that feature.

The coarse method, metric and precision lists do not imply every combination is
supported. Inspect `VectorIndexCapabilities` for physical index compatibility:

```go
for _, index := range caps.VectorIndexCapabilities {
    fmt.Printf("%s / %s / %s: up to %d dimensions\n",
        index.Method, index.Metric, index.Precision, index.MaxDimensions)
}
```

PostgreSQL reports HNSW for all four dense metrics and IVFFlat for cosine/L2/inner
product, each with float32 (2,000 indexed dimensions) and float16 (4,000).
Binary entries have `Kind=VectorBinary`, empty precision, and Hamming distance;
HNSW and IVFFlat both support up to 64,000 bits. Empty kind in older entries means
dense, and missing `VectorKinds` means the provider has not reported kinds.
Sparse entries have `Kind=VectorSparse`, float32 precision, HNSW for all four
non-Hamming metrics, `MaxDimensions=1000000000` and `MaxNonZero=1000`. Nonzero
limits count stored components, separately from declared dimensions. A zero
`MaxNonZero` means the provider has not reported that limit.
Unsupported PostgreSQL combinations fail before database access. A third-party
provider with a nil detailed list has not reported combinations; use its schema
validation rather than assuming a cross product. Memory and SQLite report no
physical indexes.

Discovery does not access the database or check deployment versions, permissions,
health, collection definitions or embedder configuration. A hybrid-capable
provider still needs the relevant model properties and options. Operations retain
their usual schema and deployment validation. Built-in stores return independent,
caller-owned slices; changing a snapshot does not configure the provider.

`ModelStore` remains unchanged. A third-party provider without
`CapabilityProvider` has unavailable metadata; this does not imply its optional
features are unsupported. Callers can still use the `HybridSearcher` interface
and normal operation errors when metadata is unavailable.

## Lifecycle and retrieval

All three providers implement `ModelStore` and `CollectionManager`. Stores can list
collections, check existence, and delete collections idempotently. PostgreSQL
listing includes registered models (even models without vectors) and existing
vector tables in the configured schema. Ordinary unrelated tables are excluded.
`CollectionExists` resolves only the requested, schema-qualified relation rather
than listing every collection. Vector tables need one catalog query; registered
models without vectors also check their registry entry. Views and missing/dropped
relations are excluded even if a stale registry entry exists. Existence checks
do not validate a collection's schema.

| Operation | Behavior |
| --- | --- |
| `Get` | Returns `ErrNotFound` for a missing key |
| `GetMany` | Returns only found records, deduplicates keys, and makes no ordering promise |
| `Query` / `QueryIter` | Filter without vector search, with `top`, `Skip`, and `OrderBy` |
| `Insert` / `Upsert` | Return native keys; all records are committed atomically |
| `Delete` | Ignores missing keys and returns the number deleted |
| `Count` | Applies the same filter language as retrieval/search |

PostgreSQL selects binary vector parameters for requests containing at least
1,024 vector values, and retains text for smaller writes. Both paths keep atomic
writes, generated IDs and returned-key ordering. Driver type mappings are scoped
to the write and restored before pool release. See the
[transport benchmarks](performance.md#binary-vector-ingestion-and-copy-comparison)
for measurements and the COPY comparison.

Reads exclude vectors by default. Use `RetrievalOptions{IncludeVectors:true}`,
`QueryOptions.IncludeVectors`, or `ModelSearchOptions.IncludeVectors` to load
all named vectors. Filtered retrieval sorts by the requested scalar properties
with a key tie-breaker; without explicit ordering it sorts by key. JSON/array
ordering is not part of the portable contract. Text collation can vary by provider.

`QueryIter` and `SearchIter` use Go iterators. PostgreSQL reads rows lazily and
releases the connection on completion, error, cancellation, or early loop exit.
Memory and SQLite materialize QueryIter snapshots and SearchIter result pages
before yielding. SQLite releases its SQL connection before application iteration;
its query/search costs are described in the [SQLite guide](sqlite.md#query-costs-and-provider-limits).
Querying a deleted/missing collection returns `ErrCollectionNotFound`. PostgreSQL
and SQLite operation errors add backend, collection, and operation context through
`OperationError`, preserving
`errors.Is`/`errors.As`.

## Filtering

Use `Column("Category")` for model properties and `DataPath("Details", "rating")`
for JSON properties. `Metadata(...)` remains the original Record API's JSON path
syntax. Nested JSON keys, including empty/whitespace keys, are literal.

Operators include `Eq`, `Ne`, `In`, `Gt`, `Gte`, `Lt`, `Lte`, `Contains`, `Exists`,
`ContainsAny`, `ContainsAll`, `And`, `Or`, and `Not`. `Contains` means array
membership, not substring matching. See [array set filters](#array-set-filters)
for overlap/containment and empty-operand semantics.
Native-array membership uses a GIN-compatible PostgreSQL containment expression,
with typed scalar parameters for native arrays, including exact decimals.

The filter contract preserves SQL-style unknown/null behavior under negation:
a missing JSON path does not match `Eq(path,nil)` or its negation. A present JSON
null does match `Eq(path,nil)`. An optional native JSON column can contain SQL
NULL (`nil`) or JSON null (`JSONNull{}`); `Column` tests native SQL null, while
`DataPath` tests JSON values. `JSONNull{}` is also accepted in JSON path equality
and membership predicates. JSON numbers and strings remain distinct for equality.
Native NULL array membership remains unknown under `Not`, while a missing/nonarray
JSON membership test is false. Invalid fields are rejected even for empty stores.

Ordered JSON comparisons against numbers use guarded numeric conversion;
missing, null and nonnumeric values compare false. Text comparisons preserve SQL
unknown for missing/null paths, including under negation. Array paths support
negative indexes and leading ASCII whitespace; trailing whitespace is invalid.
Object path segments always remain literal keys.

The AST lives in `vectordata/filterast`. PostgreSQL SQL compilation lives in
`stores/postgres/filtersql`. The old `vectordata.CompileFilterSQL` and
`FilterSQLConfig` remain deprecated compatibility shims.

## Named vectors, scores, and paging

Each vector property has its own dimensions, metric, storage name and index.
When multiple vectors exist, specify `ModelSearchOptions.VectorProperty` by its
logical name. An ambiguous selection is an error. All vector elements must be
finite; cosine vectors must be nonzero.

`Skip` and `top` page the ranked results. Approximate indexes can still return
fewer matches or change the candidate set between requests; paging does not
create a stable snapshot across separate searches.

Memory and SQLite perform exact scans, retaining the best `top + Skip` candidates
and sorting that set by distance, then native key for ties. Memory takes a shallow
snapshot of immutable stored records and copies only the returned page. SQLite
streams decoded records from one read snapshot. Both still examine the whole
collection; large offsets increase retained candidate memory. See the
[selection benchmarks](performance.md#exact-search-selection-and-copying).

Vector search returns raw `Distance` plus a higher-is-better `Score`, tagged
`ScoreSimilarity`. `DistanceThreshold` is an upper bound on raw distance;
`ScoreThreshold` is a lower bound on the library's similarity score. These are
explicit Go semantics, not interchangeable with every provider-specific .NET
score threshold. HNSW/IVFFlat tuning is passed as
`postgres.SearchTuning` through `ProviderOptions`; unknown provider options are
rejected. Transaction-local settings do not leak to other pooled requests.

For Manhattan distance, use `DistanceL1` or a `metric=l1` tag:

```go
Embedding []float32 `vectordata:"vector,dimensions=3,metric=l1,index"`
```

L1 distance is the sum of absolute component differences, and its similarity score
is `1 / (1 + distance)`. Zero vectors are valid. For example, distance 3 has score
0.25, while identical vectors have distance 0 and score 1. Distance thresholds
remain upper bounds on raw distance; score thresholds remain lower bounds on
similarity. Memory, SQLite and PostgreSQL support L1 with both precisions, including
text-generated queries and PostgreSQL hybrid search. Hybrid scores still use RRF.

PostgreSQL uses `<+>` for L1 search and `vector_l1_ops`/`halfvec_l1_ops` for HNSW.
This workflow requires pgvector 0.7+ with the required operators/classes visible.
Model creation checks their availability, and migration planning reports missing
prerequisites. L1 with an IVFFlat index returns `ErrSchemaMismatch`; IVFFlat tuning
on L1 searches returns `ErrInvalidSearchOptions` before text embedding. These rules
also apply to the float32 Record API. See
[pgvector's HNSW support](https://github.com/pgvector/pgvector#hnsw).

Adding an HNSW index to an existing L1 model is supported by additive migrations.
Changing an existing property's metric requires manual semantic review and index
rebuilds; `ApplyMigration` rejects that change.

## Vector storage precision

Vector inputs and outputs remain `[]float32`. Select half-precision storage with a
tag or explicit definition:

```go
type CompactProduct struct {
    ID        string    `vectordata:"key"`
    Embedding []float32 `vectordata:"vector,dimensions=3072,precision=float16,index"`
}

vector := vectordata.VectorProperty{
    Name: "Embedding", Dimensions: 3072, Precision: vectordata.VectorFloat16,
    Index: &vectordata.VectorIndexOptions{Method: vectordata.IndexMethodHNSW},
}
_ = vector
```

PostgreSQL maps float16 properties to `halfvec(n)` and selects `halfvec_*_ops` for
HNSW/IVFFlat indexes. Both storage types allow up to 16,000 dimensions; indexed
float32 properties allow up to 2,000 and indexed float16 properties up to 4,000.
Invalid PostgreSQL dimensions fail while constructing the collection handle.
These limits follow [pgvector's supported types](https://github.com/pgvector/pgvector#hnsw).
Memory accepts index hints without building physical indexes.

Writes, embeddings and search queries round float16 components to IEEE binary16
using nearest, ties to even. Reads expose those rounded values as float32.
Memory uses the same conversion, so it models precision loss as well as schema
validation. Non-finite values, values that overflow after rounding and cosine
vectors that become all-zero are rejected with `ErrInvalidVector` before storage
writes. Small components can round to zero; rounding can change distances, scores,
threshold matches and result ordering. Measure recall with your own embeddings.

Empty precision and explicit `VectorFloat32` preserve existing float32 behavior.
Normalization keeps that default empty and omits it from serialized registrations,
so models registered before precision support do not need metadata changes.
Precision changes on registered properties require a manual, potentially lossy
conversion; the migration planner flags them and additive `ApplyMigration` rejects
them. Index additions on existing half-precision properties can be applied.

The original Record API remains float32. Model writes reuse the existing float32
wire encoding and explicitly cast to halfvec; storage precision does not imply
16-bit wire transport. See [precision verification](performance.md#half-precision-vector-verification)
for rounding, storage and recall checks.

## Automatic embeddings

Implement `Embedder.Embed(ctx, inputs []string) ([][]float32,error)` and configure
it on the store or in `ModelCollectionOptions`. Resolution is property embedder →
collection embedder → store embedder; nil and typed-nil embedders inherit the next
default. `SearchText` and `HybridSearchText` use the same resolved embedder as writes.
Tagged models connect a vector to a string data field with `source=Description`.

Configure PostgreSQL defaults when constructing the store:

```go
opts := postgres.DefaultStoreOptions()
opts.Embedder = embedder // Your vectordata.Embedder implementation.
opts.EmbeddingBatchSize = 128
store, err := postgres.NewVectorStore(pool, opts)
```

For memory, use `memory.NewWithOptions(memory.StoreOptions{Embedder: embedder,
EmbeddingBatchSize: 128})`. Existing `memory.New()` callers retain their defaults.
Both constructors reject negative batch sizes with `ErrInvalidEmbeddingOptions`.
Override either setting per collection using `ModelCollectionOptions{Embedder:
otherEmbedder, EmbeddingBatchSize: 32}`. Zero batch size inherits the store setting;
if both are zero, writes use one request per vector property. A positive value is
a maximum input count per request, not a token or byte limit. A collection can
override the size upward or downward; zero does not disable a positive store limit.
These runtime settings are captured by collection handles and are not persisted
in model registrations or migration plans.

Writes generate missing vectors in input order, separately for each property.
Explicitly supplied vectors take precedence and do not consume embedding batches.
Requests run sequentially, with cancellation checked before and after each call.
Each batch's output count/dimensions/values are checked before another request.
The complete record batch is prepared before any storage write, so a later
embedding failure or cancellation leaves existing records untouched and inserts
no new records. Earlier embedding requests may still have consumed provider quota;
the library does not retry or undo those external calls. Text queries embed one
input regardless of batch size.
Callers must use compatible embedding models for ingestion and queries;
embedders must honor cancellation and be safe for concurrent use.

### Custom and multimodal inputs

`InputEmbedder.EmbedInputs(ctx, inputs []any) ([][]float32, error)` accepts text,
image/audio bytes, and application values. `InputEmbeddingFunc[T]` adapts a typed
function and rejects nil values or type mismatches before invoking that function
for a batch. Actual media decoding and model calls belong to your embedder.
Outputs are still dense float32 vectors, optionally stored at float16 precision;
automatic binary/sparse generation and database-side generation are unsupported.

Configure `InputEmbedder` on `StoreOptions`, `ModelCollectionOptions` or
`VectorProperty`. Resolution is property → collection → store, across both kinds
of embedder. A text embedder at a higher level replaces an inherited input embedder
and vice versa. Configure only one kind at each level; ambiguous options fail with
`ErrInvalidEmbeddingOptions` (ambiguous property definitions fail with
`ErrSchemaMismatch`). Nil and typed-nil values inherit the next default.

For example, your application can supply image bytes:

```go
embedder := vectordata.InputEmbeddingFunc[[]byte](
    func(ctx context.Context, images [][]byte) ([][]float32, error) {
        return imageModel.Embed(ctx, images) // Your application's model adapter.
    })
options := vectordata.ModelCollectionOptions{InputEmbedder: embedder, EmbeddingBatchSize: 32}
```

A dense vector field of type `string` is an embedding input automatically. Other
input types require the `input` tag, including custom structs and image/audio
byte slices. Optional string pointers supply the dereferenced string; other
custom pointer inputs retain their type. The field's type must match the typed
adapter. Define separate vector properties or a common application input type
when a generator needs to accept multiple media types.

```go
type Photo struct {
    ID    string `vectordata:"key"`
    Title string `vectordata:"data"`
    Image []byte `vectordata:"vector,input,dimensions=1536"`
}
```

Input fields are never stored or decoded: they have their zero value on reads,
even with `IncludeVectors`. Add separate data properties for sources you need to
retrieve. A normal `[]float32` field with the same logical name can read the stored
vector using the same definition. The `input` tag changes mapping, not schema.
It cannot be combined with `source` or a binary/sparse vector kind.

Dynamic records supply transient values in `ModelRecord.EmbeddingInputs`, keyed
by dense vector property name. This map is excluded from JSON and consumed during
batch preparation; providers never store or return it. Explicit vectors take
precedence over input values. Otherwise a transient input overrides `SourceProperty`;
the latter can name any native data property. Native sources reach the embedder
in canonical form (for example, JSON objects are maps rather than custom structs).
The original text `Embedder` continues to require a string source. Unknown input
properties and inputs for binary/sparse properties fail before any embedding call.
A nil input omits an optional vector; it is an error for a required vector.

Built-in collections implement optional `InputSearcher.SearchInput` and PostgreSQL
also implements `InputHybridSearcher.HybridSearchInput`. The typed wrapper exposes
both methods and returns `ErrUnsupported` for third-party providers without those
interfaces. `SearchText` and `HybridSearchText` remain text convenience methods and
use the same resolved generator. Queries always embed one input. Options and
filters are validated before invoking a generator; existing batch limits, output
validation, cancellation, and atomic write guarantees apply to input embeddings.

Mutable inputs are borrowed read-only during the call: generators must not mutate
or retain them, and callers must not change them concurrently. The library copies
generated vectors before storing or returning them, so generators may reuse output
buffers on later calls. Runtime generators never enter registrations or migration
plans. See the runnable [custom-input sample](../samples/input-embeddings/README.md).

## Binary vectors

Binary vectors are explicit model properties with packed bytes and an exact bit
length. They support Hamming distance in all three providers:

```go
type Image struct {
    ID          string                  `vectordata:"key"`
    Description string                  `vectordata:"data,fulltext"`
    Fingerprint vectordata.BinaryVector `vectordata:"vector,kind=binary,dimensions=9,index"`
}
bits, err := vectordata.ParseBinaryVector("101010101")
_ = bits // Bytes: {0xaa, 0x80}, Dimensions: 9.
_ = err
```

Explicit definitions use `VectorProperty{Kind:VectorBinary, Dimensions:9}`.
The metric defaults to `DistanceHamming`; other metrics and float precisions are
rejected for binary properties. Dense remains the default kind, and existing
registrations retain their original JSON representation.

`BinaryVector{Bytes, Dimensions}` stores bits most significant bit first. Use
`NewBinaryVector(bytes, dimensions)` to validate/copy packed bytes, or
`ParseBinaryVector` for 0/1 text. `BitString()` returns the exact bits. Dimensions
must be 1–64,000, the byte count must be `ceil(dimensions/8)`, and unused low bits
must be zero. Non-byte-aligned vectors and all-zero vectors are valid. Wrong bit
lengths return `ErrDimensionMismatch`; invalid padding returns `ErrInvalidVector`.

Dynamic records put binary values in `ModelRecord.BinaryVectors` and dense values
in `ModelRecord.Vectors`; mixing up maps or omitting a required vector fails
before writes. Models can contain multiple dense and binary properties. Providers
copy binary buffers on writes and reads. `IncludeVectors` controls both maps;
typed binary fields stay zero-valued when vectors are omitted.

Typed collections expose `SearchBinary(ctx, bits, top, opts)` and
`SearchBinaryIter`. Dynamic collections implement the optional `BinarySearcher`
interface, leaving the existing `ModelCollection` interface unchanged. Set
`VectorProperty` when the model has multiple vector fields. Filtering, paging,
thresholds, cancellation and projections follow the dense search contract.
Distance counts differing bits; similarity is `1/(1+distance)`.

PostgreSQL stores `bit(n)` and supports HNSW/IVFFlat `bit_hamming_ops` indexes
with pgvector 0.7+ and visible operators/classes. Packed bits travel through the
driver directly for writes, queries and reads. Query parameter casts preserve
the declared bit length; indexed columns remain native. PostgreSQL also supports
`BinaryHybridSearcher`/typed `HybridSearchBinary`, combining Hamming candidates
and keyword candidates with the existing RRF semantics.

Float-based `Search`/`SearchIter` require a dense property; binary search requires
a binary property. Binary values are supplied explicitly: binary properties do
not use the float `Embedder` or `source` mapping, and text query methods reject a
selected binary property before invoking an embedder. Dense embedding generation
continues to work in mixed models. Adding binary fields or converting existing
vectors requires manual backfill/re-embedding review. New compatible indexes can
be applied through additive migrations. Prerequisite and schema checks validate
operator/index availability and exact bit lengths.

## Sparse vectors

Sparse vectors store only nonzero coordinates and support cosine, L2, inner
product and L1 distance in all three providers:

```go
type Document struct {
    ID       string                  `vectordata:"key"`
    Body     string                  `vectordata:"data,fulltext"`
    Features vectordata.SparseVector `vectordata:"vector,kind=sparse,dimensions=1000000,index"`
}
features, err := vectordata.NewSparseVector(1_000_000,
    []int{0, 999_999}, []float32{1, 0.5})
_ = features
_ = err
```

`SparseVector{Dimensions, Indices, Values}` uses zero-based indices in strictly
increasing order, with no duplicates. Coordinate slices must have equal lengths;
values must be finite float32 numbers and indices must be within dimensions.
`NewSparseVector` validates/copies input and removes explicit zeros. The maximum
dimension count is 1 billion and input coordinate lists support up to 16,000
entries. HNSW properties permit at most 1,000 nonzero values after removing zeros.
Cosine rejects zero vectors; L2, L1 and inner product accept them.
`NormalizeSparseVector` applies property/index validation; `SparseDistance`
computes distances in O(nonzeros) time without allocating dense arrays.

Explicit schemas set `VectorProperty.Kind=VectorSparse`. Cosine is the default
metric and float32 is the only precision. Typed vector fields use `SparseVector`;
dynamic records use `ModelRecord.SparseVectors`, alongside the existing dense and
binary maps. Models can mix all three kinds and multiple sparse properties.
Providers copy coordinate buffers on writes and reads, and `IncludeVectors`
controls sparse projections along with other vectors. Omitted typed sparse
fields stay zero-valued.

Typed collections expose `SearchSparse(ctx, features, top, opts)`,
`SearchSparseIter` and `HybridSearchSparse`. Dynamic collections use optional
`SparseSearcher`/`SparseHybridSearcher` interfaces. Named-vector selection,
filters, paging, thresholds, cancellation, similarity scores and iterators follow
the existing model search contract. PostgreSQL supports sparse/keyword RRF hybrid
search; memory provides exact sparse search. Floating-point arithmetic can differ
slightly between providers, as with dense search.

PostgreSQL uses `sparsevec(n)` with pgvector 0.7+ and visible type, distance
operators and HNSW classes. The connector translates zero-based Go coordinates
to one-based PostgreSQL text coordinates without expanding omitted dimensions.
IVFFlat indexes/tuning and float16 precision are rejected for sparse properties.
Sparse values are explicit: float `Embedder`/`source` mappings and text-query
embedding are not used for a sparse property. Dense embedding generation still
works in mixed models. Existing dense/binary APIs and registrations are unchanged.

Adding sparse fields or converting representations requires manual backfill and
re-embedding review. Compatible HNSW index additions are additive when existing
rows meet the nonzero limit. Planning checks stored sparse serialization sizes;
oversized rows add a manual backfill step. Application rechecks data under its
existing locking/review rules and rejects a stale review if new data makes the
index incompatible. See [migrations](migrations.md).

## Hybrid search

PostgreSQL implements optional `HybridSearcher`. Declare a string data property
with `IsFullTextIndexed` (or the `fulltext` tag), then call `HybridSearch` or
`HybridSearchText`. Specify `TextProperty` if more than one field is full-text
indexed, and `VectorProperty` if more than one vector is defined. Invalid paging,
thresholds, keywords, properties, filters and local provider settings are rejected
before `HybridSearchText` invokes the embedder; an already-canceled request also
skips embedding.

Keyword search uses PostgreSQL's `simple` text-search configuration and
`plainto_tsquery`. Filters apply to both the vector and keyword candidate sets.
Reciprocal rank fusion combines the rankings with a default constant of 60.
`CandidateLimit` bounds each candidate list and must cover `top + Skip`.
Results have `ScoreKind=ScoreRRF`; their score thresholds apply to fusion scores,
not vector similarity. Raw vector distance is still returned. Fusion scores
should not be compared with ordinary vector similarity scores.

The memory and SQLite providers perform exact vector search and do not implement hybrid
search. Typed calls return `ErrUnsupported` for this optional capability. Index
hints are accepted for portability but do not create physical indexes in either provider. See [SQLite storage and limits](sqlite.md).

## Verification and scope

`vectordata/conformance.Run` exercises shared provider behavior. Memory and SQLite tests
and PostgreSQL integration tests run it, including key types, identity keys,
filters, nulls, projections, paging, thresholds, atomicity, and lifecycle.
PostgreSQL-specific tests cover typed mapping, embedding generation, hybrid
search, index conflicts, concurrent initialization and early iterator cleanup.
`conformance.RunFilterParity` compares generated filter trees between two providers;
expected-result edge cases also run as part of `conformance.Run`. See
[performance and verification](performance.md) for benchmark commands, local
measurements and model index-plan checks. Keep production database integration
tests even when using memory for development.

This is a Go adaptation of the main model-driven workflows. See the
[parity checklist](parity.md) for supported workflows, Go-specific differences,
and remaining provider limitations. Existing Record API callers can adopt models
collection by collection.


## Optional vectors

Set `VectorProperty.Optional=true`, tag a dense slice with `optional`, or use
`*[]float32`, `*BinaryVector`, or `*SparseVector` fields. Pointer vector fields
infer optionality; explicit definitions must also mark them optional. The default
remains required for compatibility with existing registrations.

```go
type PartialDocument struct {
    ID string `vectordata:"key"`
    Embedding []float32 `vectordata:"vector,dimensions=3,optional"`
    Bits *vectordata.BinaryVector `vectordata:"vector,dimensions=9,kind=binary"`
    Sparse *vectordata.SparseVector `vectordata:"vector,dimensions=1000,kind=sparse"`
}
```

In dynamic records, absent/nil dense slices and missing binary/sparse map entries
represent NULL. Non-nil empty dense slices and present zero-valued binary/sparse
structs are validated and rejected. A valid zero sparse vector for L2/L1/inner
product is still a present vector. IncludeVectors reads omit missing entries;
typed reads preserve nil pointers and slices. Writes replace the complete record,
so an upsert with a missing optional vector clears the previous value.

When an optional dense property has a `SourceProperty`, a missing vector is
embedded when its source is a string. A missing/NULL source leaves the vector
NULL. An explicit non-nil malformed vector fails validation before embedding.
Required-vector embedding behavior is unchanged.

All vector search variants exclude records without the selected property before
ranking, thresholds, and paging. PostgreSQL hybrid search also excludes them from
both candidate lists, including otherwise matching keyword records. Other vector
properties may be absent in a returned record. Query, Get, and Count retain such
records. Search queries themselves still require a valid vector.

PostgreSQL creates nullable native vector/halfvec/bit/sparsevec columns. Adding a
new optional vector is an additive migration; existing nullability changes need a
separate reviewed procedure. See [migration planning](migrations.md).

## Array set filters

```go
any := vectordata.ContainsAny(vectordata.Column("Tags"), "quiet", "nature")
all := vectordata.ContainsAll(vectordata.Column("Tags"), "quiet", "nature")
jsonAny := vectordata.ContainsAny(vectordata.DataPath("Details", "tags"), "quiet")
```

Operands are variadic elements, not a whole slice; expand `[]any` with `...`.
Native array operands use the same element normalization as Contains, including
UUIDs, timestamp/calendar values, and exact decimals. Duplicates do not change
results. PostgreSQL compiles native arrays to `&&` (any) and `@>` (all), allowing
GIN indexes on indexed native array properties to participate. JSON paths compare
whole array elements with the existing Contains equality semantics; nested JSON
arrays are single elements, rather than flattened containment.

Any with zero operands is false for non-NULL native arrays; All is true. Native
NULL arrays yield SQL UNKNOWN for both, including under Not, and do not match.
A native NULL operand never matches an element. JSON null elements can match a
nil operand. Missing, scalar, or null JSON paths return false for both helpers;
All with zero operands matches JSON arrays, including empty arrays.
