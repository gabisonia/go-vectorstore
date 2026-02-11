# RAGrimosa Sample

`RAGrimosa` is a retrieval-augmented generation sample built on `go-vectorstore`.

It uses the Lacrimosa story as source data, manually split into chunks and inserted into Postgres (`pgvector`).
When you ask a question, the app:

1. Retrieves relevant chunks from DB using OpenAI embeddings.
2. Sends only retrieved context to OpenAI chat completion.
3. Prints the grounded answer.

## Run with Docker Compose

```bash
cd samples/ragrimosa
cp .env.example .env
# edit .env and set OPENAI_API_KEY

docker compose up -d postgres
docker compose --profile app run --rm app
# or override question
docker compose --profile app run --rm app -q "What happened to Rex in his final period?"
```

Note:
- The sample image builds with `golang:1.24-alpine` to match the repository Go version (`1.24.x`).

Shutdown:

```bash
docker compose down
```

## What it does

1. Builds embeddings for hardcoded Lacrimosa story chunks.
2. Ensures a vector collection in Postgres.
3. Upserts chunk records and ensures vector/metadata indexes.
4. Embeds your prompt and retrieves top matching chunks.
5. Calls OpenAI chat completion with retrieved context only.
