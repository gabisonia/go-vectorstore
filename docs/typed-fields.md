# Typed filter and ordering references

`vectordata-gen` is an optional Go 1.25+ tool for tagged struct models. It emits
property references that build the existing `vectordata.Filter` AST and
`SortField` values. Providers, collection interfaces and stored schemas stay the
same. The tool is available on master after v0.4.1; use the same library/tool
revision and commit generated source alongside your model.

In the package containing the model:

```go
//go:generate go run github.com/gabisonia/go-vectorstore/cmd/vectordata-gen -type Product

type Product struct {
    ID     string   `vectordata:"key"`
    Name   string   `vectordata:"data,name=product_name"`
    Price  *int64   `vectordata:"data"`
    Tags   []string `vectordata:"data"`
}
```

Run `go generate ./...`. The default output is `product_fields.gen.go`, with a
`ProductFields` variable. `-output fields.gen.go` and `-name Fields` customize
those names. The generator uses the current module, dependencies and build
configuration; only one local struct type is generated per invocation. Install
the tool with `go install .../cmd/vectordata-gen@<your-library-version>` if you
prefer invoking `vectordata-gen` directly in the directive.

```go
filter := vectordata.And(
    ProductFields.Price.Gte(100),
    ProductFields.Tags.ContainsAny("office", "quiet"),
    ProductFields.Name.In("Keyboard", "Mouse"),
)
rows, err := products.Query(ctx, filter, 20, vectordata.QueryOptions{
    OrderBy: []vectordata.SortField{
        ProductFields.Price.Desc(), ProductFields.ID.Asc(),
    },
})
```

`ProductFields.Pirce` and `ProductFields.Price.Eq("expensive")` fail compilation.
Names are logical Go field names, so `Name` still refers to `Column("Name")`
when the storage alias is `product_name`. Embedded visible fields are included;
vectors and transient embedding inputs are omitted. Reference types are private
and contain no mutable property names.

| Property | Generated operations |
| --- | --- |
| Key and data | `Name`, `Eq`, `Ne`, `In`, `IsNull`, `IsNotNull` |
| Ordered scalar, key | `Gt`, `Gte`, `Lt`, `Lte`, `Asc`, `Desc` |
| Boolean | `Asc`, `Desc`; no ordered comparison |
| Native array | `Contains`, `ContainsAny`, `ContainsAll`; no ordering |
| JSON | `Path`, `IsJSONNull`; no ordering |

Operands use the model's Go type, preserving named scalar and native-array element
types. Pointer properties accept their dereferenced value; use `IsNull` or
`IsNotNull` for SQL NULL. Existing distinctions between missing values, SQL NULL,
JSON null and empty arrays apply. `IsJSONNull` compares with `JSONNull{}`.
`Path` returns a dynamic `FieldRef`; JSON path segments and values remain checked
at runtime. Combine generated filters with ordinary `And`, `Or`, `Not`, `Column`
and `DataPath` expressions. The AST does not carry a model type: combining
references from different models still requires runtime validation.

## Regeneration and validation

Run the generator after editing the model. Compiler anchors detect removed,
renamed or Go-type-changed referenced fields in stale output. Tags and new fields
also require regeneration; check freshness in CI from the model package:

```sh
go run github.com/gabisonia/go-vectorstore/cmd/vectordata-gen -type Product -check
```

Use the same `-output` and `-name` options as the directive. `-check` reports stale
or missing output without writing files. Ordinary generation preserves unchanged
output and refuses to overwrite a file without the matching generator/model
header. Generated source is replaced atomically after formatting.

Generation reads/type-checks source and dependency export data with the Go
toolchain; it never executes model package initialization. Missing generated
references elsewhere in application code do not prevent initial generation, and
stale output does not prevent regeneration. Unresolved model field types fail.
Build dependencies and any toolchain download follow normal Go configuration.

This first increment supports tagged struct models, including aliases to concrete
generic instantiations. It does not infer custom codec mappings or runtime schema
overrides. Packages shadowing predeclared identifiers are rejected. Schema/tag
validity, numeric ranges, finite floats, timestamp/UUID/calendar/decimal formats,
JSON serialization and provider support remain runtime checks through the
existing model API. In particular, inferred legacy `string_array` storage still
requires the existing supported `[]string` representation; generation does not
expand the codec's accepted types.

See [the runnable sample](../samples/typed-fields/README.md) for query, count,
filtered vector search, storage aliases, nullable values and SQLite reopen.
