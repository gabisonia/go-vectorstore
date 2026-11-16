# Custom embedding inputs

```sh
go run ./samples/input-embeddings
```

Expected output:

```text
Cat: distance=0, input retained=false
```

This credential-free sample maps an application struct containing a caption and
image bytes to a dense vector with `InputEmbeddingFunc[ImageInput]`. It uses a
deterministic fixture; replace that function with an actual image/multimodal
embedding model for semantic search. No files or remote resources are created;
the memory store disappears when the program exits. The sample is rerunnable.

The `vector,input` tag sends the field to the embedder during writes. `SearchInput`
uses the same configured generator for queries. Input fields are transient and
are zero-valued on reads; add a separate `data` field if the source must be stored.
An ordinary string vector field does not require the `input` flag and also works
with the original text `Embedder`. The source type must match the generic adapter,
including custom pointer types; optional string pointers supply their string value.

All three built-in providers support custom inputs. See the
[model API](../../docs/model-api.md#custom-and-multimodal-inputs) for configuration,
dynamic records, ownership and error behavior. These APIs are on master after
v0.4.1; consult [release guidance](../../docs/releases.md) for published versions.
