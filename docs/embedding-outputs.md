# Embedding output representations

The existing `Embedder` and `InputEmbedder` interfaces return dense float32
vectors. Keep them compatible: provider contracts, stored definitions, canonical
records and query results remain unchanged. The additive output adapter converts
a generator's representation into that existing boundary.
Model vector fields remain `[]float32`; `Float16Bits` is a generator output type.

## Contract and conversion decision

Microsoft's [VectorStoreVectorProperty.EmbeddingType](https://source.dot.net/Microsoft.Extensions.VectorData.Abstractions/RecordDefinition/VectorStoreVectorProperty.cs.html)
selects a desired generated embedding type, including `Embedding<Half>`.
Its [Embedding abstraction](https://learn.microsoft.com/en-us/dotnet/api/microsoft.extensions.ai.embedding)
also describes float32/float64, integer and binary representations. The 2026-11-18
audit informs this design; it does not imply every .NET connector supports every
representation or that Go should duplicate its reflection API.

Separate three decisions:

| Decision | Go contract | Persistence |
| --- | --- | --- |
| Input type | `OutputEmbedder[TInput, TElement]` and existing text/custom-input adapters | Existing model data or transient `EmbeddingInputs` |
| Generator output | Generic `TElement`: float32 (including named aliases) or explicit `Float16Bits` | Runtime adapter only |
| Stored precision | Existing `VectorProperty.Precision` | Existing normalized registration and canonical format |

`Float16Bits` is an IEEE binary16 bit pattern, not a numeric uint16 embedding.
Raw uint16, float64, integer-quantized, binary and sparse generator outputs are
outside this first adapter contract. There is no implicit integer interpretation,
binarization, sparse densification, quantization scale or precision inference.
Existing explicit binary/sparse CRUD and query APIs are unchanged.

`AdaptOutputEmbedder` returns an `InputEmbedder` and requires a non-nil source.
The source receives a typed input batch, produces one vector per input in order,
honors context cancellation, and supports concurrent use. Inputs are borrowed;
do not mutate/retain them. Output buffers can be reused on the next call because
the adapter copies/converts every vector before returning it. Concurrent calls
must not share mutable output buffers.
The application owns the source and its client lifetime. Keep them alive while
collections use the adapter, and close them after in-flight work has finished.

All input types are checked before calling the source. Nil inputs, wrong input
types or a typed-nil source fail with `ErrInvalidEmbeddingOptions`. Empty batches
return an empty result without invoking the generator. Generator errors preserve
their native chains; cancellation is checked before and after the call.
Wrong output counts, empty vectors, NaN and infinities fail with
`ErrInvalidVector`. Property dimension/metric validation remains in the existing
preparation/query pipeline, including zero cosine vectors and float16 overflow.
No retry or provider write is added by the adapter.

## Provider support

| Generator output | Canonical float32 storage | Canonical float16 storage |
| --- | --- | --- |
| float32 | Existing behavior; copied finite values | Existing nearest/ties-to-even rounding; overflow rejected |
| Float16Bits | Exact widening, including subnormals and signed zero | Exact widening followed by existing idempotent normalization |

Memory, SQLite, sqlite-vec, PostgreSQL, Redis and the bounded Qdrant foundation
share this dense input path. Physical index support continues to come from each
provider's capabilities and deployed backend. The adapter does not advertise a
new native index, storage type or automatic binary/sparse capability.

Use the adapter in exactly one existing `InputEmbedder` slot: store defaults,
collection options or a vector property. Existing precedence and bounded batching
apply without introducing another resolution hierarchy. Reattach the same
application generator/model when reopening. Equal dimensions/representations do
not establish embedding-model compatibility.

```go
source := vectordata.OutputEmbeddingFunc[string, vectordata.Float16Bits](generateHalf)
adapter, err := vectordata.AdaptOutputEmbedder(source)
if err != nil { return err }
options := vectordata.ModelCollectionOptions{InputEmbedder: adapter}
```

Here `generateHalf` has signature
`func(context.Context, []string) ([][]vectordata.Float16Bits, error)`. The
[runnable sample](../samples/output-embeddings/README.md) supplies the complete
fixture; the executable `ExampleAdaptOutputEmbedder` covers direct adaptation.

## Acceptance checks

- Exhaustively compare all 65,536 float16 bit patterns against an independent
  arithmetic oracle, including subnormals, infinities, NaNs and signed zero.
- Check malformed counts/values, cancellation, error chains, typed nils, input
  types and output-buffer ownership without a provider or network service.
- Run shared embedding conformance for float16 output with float32/float16
  storage, batching, failed-later-batch atomicity and text/custom-input queries.
- Reopen an unchanged SQLite registration with the runtime adapter attached;
  compare unchanged serialized definitions and original float32 behavior.
- Run minimum/current Go, real backend conformance, documentation and sample
  checks before merging the implementation.

Future output representations need separate conversion/range/ownership decisions
and provider validation. Adding a type parameter to the generator does not by
itself authorize lossy conversion or expand a provider's capabilities.
