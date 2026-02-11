# Semantic Search Sample

This sample indexes 3 fake articles into Postgres (`pgvector`) and runs semantic search using OpenAI embeddings.

## Run With Docker Compose (app in container)

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

Shutdown:

```bash
docker compose down
```

## Run On Host (go run)

```bash
cd samples/semantic-search
export OPENAI_API_KEY=your_key_here

docker compose up -d postgres
go run . -q "how can I reduce cloud costs?" -category backend
```

When running on host, default `PG_DSN` is:

```text
postgres://postgres:postgres@localhost:54329/vectorstore_test?sslmode=disable
```

## CLI flags

- `-q`: semantic search query
- `-collection`: collection name (default `sample_articles`)
- `-category`: optional metadata category filter

## Environment variables

- `OPENAI_API_KEY` (required)
- `OPENAI_EMBEDDING_MODEL` (optional, default `text-embedding-3-small`)
- `OPENAI_BASE_URL` (optional, default `https://api.openai.com/v1`)
- `PG_DSN` (optional; defaults to local compose Postgres on port `54329`)

## What it does

1. Generates embeddings for 3 hardcoded fake articles
2. Ensures a vector collection in Postgres
3. Upserts records and ensures vector/metadata indexes
4. Embeds your query and returns the top 3 nearest articles
