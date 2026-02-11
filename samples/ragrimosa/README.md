# RAGrimosa Sample

`RAGrimosa` is a retrieval-augmented generation sample built on `go-vectorstore`.

It uses the Lacrimosa story as source data, manually split into chunks and inserted into Postgres (`pgvector`).
When you ask a question, the app:

1. Retrieves relevant chunks from DB using OpenAI embeddings
2. Sends only retrieved context to OpenAI chat completion
3. Prints the grounded answer

## Run With Docker Compose (app in container)

```bash
cd samples/ragrimosa
cp .env.example .env
# edit .env and set OPENAI_API_KEY

docker compose up -d postgres
docker compose --profile app run --rm app
# or override question
docker compose --profile app run --rm app -q "What happened to Rex in his final period?" -topk 4
```

Shutdown:

```bash
docker compose down
```

## Run On Host (go run)

```bash
cd samples/ragrimosa
export OPENAI_API_KEY=your_key_here

docker compose up -d postgres
go run . -q "How did Lacrimosa and Rex build trust?" -topk 4
```

When running on host, default `PG_DSN` is:

```text
postgres://postgres:postgres@localhost:54330/vectorstore_test?sslmode=disable
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
- `PG_DSN` (optional; defaults to local compose Postgres on port `54330`)

## What it does

1. Builds embeddings for hardcoded Lacrimosa story chunks
2. Ensures a vector collection in Postgres
3. Upserts chunk records and ensures vector/metadata indexes
4. Embeds your prompt and retrieves top matching chunks
5. Calls OpenAI chat completion with retrieved context only
