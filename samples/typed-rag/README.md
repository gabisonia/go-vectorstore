# Typed retrieval-augmented generation

[main.go](main.go) upserts typed articles with automatic embeddings, calls
`SearchText`, builds sources with stable `[IDs]`, and optionally generates an
answer. Retrieval and generation remain explicit application steps.

Run from the repository root with Go 1.25+, without credentials or a server:

```sh
go run ./samples/typed-rag
go run ./samples/typed-rag -sqlite /tmp/typed-rag.db -top 1
```

The fixture retrieves sources headed by `[costs] Reducing cloud costs`, then prints:

```text
Fixture mode: retrieved sources only; use -openai for generation.
```

The deterministic keyword embedder demonstrates the API. It is not a semantic
model and fixture mode does not generate an answer.

For real embeddings and generation, export `OPENAI_API_KEY` and run:

```sh
go run ./samples/typed-rag -openai -collection typed_rag_openai -q 'How can we reduce cloud costs?'
```

This makes billable remote requests. Defaults are `text-embedding-3-small` with
1,536 dimensions and `gpt-4o-mini` through Chat Completions. Configure compatible
models with `OPENAI_EMBEDDING_MODEL`, `OPENAI_CHAT_MODEL`, `OPENAI_BASE_URL`, and
`-dimensions`. See [embedding adapter requirements](../typed-search/README.md).
Use `-dsn` for PostgreSQL or `-sqlite` for a file; otherwise storage is transient
memory. Other flags are `-q`, `-top` (positive), and `-collection`.

Generation receives only the retrieved source IDs/titles/bodies, with instructions
to treat source content as data, cite IDs, and acknowledge insufficient evidence.
No generation request is made for an empty result. These instructions are an
illustration, not a guarantee of factual answers or resistance to prompt injection.
The demo has no corpus-specific relevance threshold; production applications must
choose and evaluate retrieval, authorization, and answer policies for their data.

The sample upserts three stable IDs. SQLite/PostgreSQL records remain after exit;
reruns replace those IDs. Give fixture and live models separate collections, even
when they have the same dimensions. Cleanup is the same as in
[typed search](../typed-search/README.md). Real answer text and scores can vary.

[Tests](main_test.go) exercise fixture mode and the complete memory-store RAG flow
against a local HTTP stand-in: batched/reordered embeddings, cited context and a
chat response. They need no API key and make no calls to OpenAI.
