# Semantic Search Sample

This sample indexes **3 fake articles** into Postgres (`pgvector`) and runs semantic search using OpenAI embeddings.

## Run with Docker Compose

```bash
cd samples/semantic-search
cp .env.example .env
# edit .env and set OPENAI_API_KEY

docker compose up -d postgres
docker compose --profile app run --rm app -q "how can I reduce cloud costs?"
```

Optional metadata filter:

```bash
docker compose --profile app run --rm app -q "go cancellation patterns" -category backend
```

Note:
- The sample image builds with `golang:1.24-alpine` to match the repository Go version (`1.24.x`).

Shutdown:

```bash
docker compose down
```

## What it does

1. Generates embeddings for 3 hardcoded fake articles.
2. Ensures a vector collection in Postgres.
3. Upserts records and ensures vector/metadata indexes.
4. Embeds your query and returns the top 3 nearest articles.
