# Typed integer and generated UUID keys

Run from the repository root:

```sh
go run ./samples/native-keys
```

Expected output:

```text
Typed int32 key: 2147483647
Unsigned key: 18446744073709551615
Generated UUID: true; caller ID unchanged: true
Retrieved: updated version
```

The sample uses an in-memory store, requires no credentials and creates no files.
It is rerunnable; records disappear when the program exits. These key APIs are available from v0.5.0.

Unsigned keys are inferred from `uint64` model fields and preserve every bit,
including values above the signed 64-bit range. `key,type=uuid,generated` generates
a UUID for a zero model key. Assign the returned key before upserting the same
model again; input models are not mutated.

Non-generated integer keys can also use `int8`, `int16`, `int32`, `int`, `uint8`,
`uint16`, `uint32`, `uint` and named types. The typed key parameter matches the
model field here: `TypedModelCollection[int32, Sensor]`. Storage remains canonical
int64/uint64. A custom codec returning a key outside the typed key's range fails
before the provider receives the batch. Generated integer keys require 64-bit
fields and typed keys.

The portable model providers support these keys. PostgreSQL stores unsigned keys as
`numeric(20,0)` and UUID keys as `uuid`. UUIDs are generated in the application
before insertion; integer identity generation remains separate. SQLite preserves
unsigned JSON numbers exactly when a file is reopened. See the
[model API](../../docs/model-api.md#schema-definitions-and-tags) for supported key
types, generation rules and storage differences.
