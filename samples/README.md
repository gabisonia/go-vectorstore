# Samples

Runnable end-to-end demos live here.

- [`semantic-search`](semantic-search): indexes fake articles and runs vector similarity search
- [`ragrimosa`](ragrimosa): retrieval-augmented generation demo using a chunked story corpus

## Model-driven collections

[`model-store`](model-store/README.md) demonstrates the typed model API with
struct tags and automatic embeddings. Run `go run ./samples/model-store` with no
API key, or pass `-dsn` to use PostgreSQL.
