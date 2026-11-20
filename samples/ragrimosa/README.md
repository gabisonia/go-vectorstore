# RAGrimosa Sample

This example uses the supported original PostgreSQL Record API. For new model
applications, see [typed-rag](../typed-rag/README.md), which also has a
credential-free fixture mode.

`RAGrimosa` is a retrieval-augmented generation sample built on `go-vectorstore`.

It uses the Lacrimosa story as source data, manually split into chunks and inserted into Postgres (`pgvector`).
When you ask a question, the app:

1. Retrieves relevant chunks from DB using OpenAI embeddings
2. Sends only retrieved context to OpenAI chat completion
3. Prints the grounded answer

## Run with Aspire

```bash
# From the repository root; requires Aspire 17, Go 1.26+ and Docker.
export OPENAI_API_KEY=your_key_here
VECTORSTORE_SAMPLE=ragrimosa QUERY='What happened to Rex in his final period?' TOP_K=4 \
  aspire run --apphost samples/aspire-services/apphost.go
```

After pgvector is healthy, deliberately start **ragrimosa** in the dashboard or
from another terminal. Explicit start prevents cloud calls merely from launching
the AppHost:

```sh
aspire resource ragrimosa start --apphost samples/aspire-services/apphost.go
aspire logs ragrimosa --apphost samples/aspire-services/apphost.go
```

Shutdown:

```bash
aspire stop --apphost samples/aspire-services/apphost.go
```

## Run On Host (go run)

```bash
export OPENAI_API_KEY=your_key_here
aspire start --apphost samples/aspire-services/apphost.go
aspire wait postgres --apphost samples/aspire-services/apphost.go
go run ./samples/ragrimosa -q "How did Lacrimosa and Rex build trust?" -topk 4
```

When running on host, default `PG_DSN` is:

```text
postgres://postgres:postgres@localhost:54329/vectorstore_test?sslmode=disable
```

## CLI flags

- `-q`: user question for RAG
- `-collection`: collection name (default `ragrimosa_story`)
- `-topk`: number of retrieved chunks (default `4`)

## Environment variables

- `OPENAI_API_KEY` (required)
- `OPENAI_EMBEDDING_MODEL` (optional, default `text-embedding-3-small`)
- `OPENAI_CHAT_MODEL` (optional, default `gpt-4o-mini`)
- `OPENAI_BASE_URL` (optional, default `https://api.openai.com/v1`)
- `PG_DSN` (optional explicit DSN; otherwise pgx reads Aspire's PG variables, or defaults to the local development database on `54329`)
- `QUERY` / `TOP_K` configure the managed Aspire resource's CLI arguments

Stop the shared AppHost before switching its mode or changing managed arguments.
See [shared Aspire workflows](../aspire-services/README.md) for credentials,
persistence, resource inspection and cleanup. Native execution remains Go 1.26+;
Go AppHost orchestration needs Go 1.26+. OpenAI requests require explicit
credentials and can incur charges.

## What it does

1. Builds embeddings for hardcoded Lacrimosa story chunks
2. Ensures a vector collection in Postgres
3. Upserts chunk records and ensures vector/metadata indexes
4. Embeds your prompt and retrieves top matching chunks
5. Calls OpenAI chat completion with retrieved context only
