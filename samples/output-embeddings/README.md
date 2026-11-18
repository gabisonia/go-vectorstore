# Typed embedding outputs

Run from the repository root with Go 1.25+, without a server or API key:

```sh
go run ./samples/output-embeddings
```

Expected output:

```text
Generated float16; canonical float32 vector: [1 0.5]
Nearest: guide (distance 0.000)
```

The fixture returns explicit IEEE binary16 `Float16Bits`, then
`AdaptOutputEmbedder` widens them exactly into the existing dense input contract.
The model's `[]float32` field and default float32 storage remain unchanged.
Use `precision=float16` or an explicit definition to select stored half precision
independently. This fixture demonstrates the API, not semantic understanding.

Configure the result in one existing `InputEmbedder` slot: store, collection or
vector-property settings. Property settings override collection/store defaults;
existing batching and explicit-vector bypass still apply. The generic input type
can also be bytes or an application value. The output type accepts float32 aliases
and `Float16Bits`; raw uint16 is not inferred to mean half precision.

The adapter copies output buffers and rejects malformed counts, empty/non-finite
vectors and invalid inputs. Property validation still checks dimensions and
metric/storage constraints. Generator errors/cancellation are preserved, and the
adapter never retries. Memory records disappear on exit. See the
[representation contract and provider matrix](../../docs/embedding-outputs.md).
