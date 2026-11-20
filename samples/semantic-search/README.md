# Semantic Search Sample

This example uses the supported original PostgreSQL Record API. For new model
applications, see [typed-search](../typed-search/README.md), which also has a
credential-free fixture mode.

This sample indexes 3 fake articles into Postgres (`pgvector`) and runs semantic search using OpenAI embeddings.

## Run with Aspire

```bash
# From the repository root; requires Aspire 17, Go 1.26+ and Docker.
export OPENAI_API_KEY=your_key_here
VECTORSTORE_SAMPLE=semantic-search QUERY='how can I reduce cloud costs?' \
  aspire run --apphost samples/aspire-services/apphost.go
```

Aspire waits for pgvector and injects its connection settings. The application
uses **explicit start** so the AppHost alone does not invoke a paid API. Start
**semantic-search** in the dashboard, or from another terminal:

```bash
aspire resource semantic-search start --apphost samples/aspire-services/apphost.go
aspire logs semantic-search --apphost samples/aspire-services/apphost.go
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
go run ./samples/semantic-search -q "how can I reduce cloud costs?" -category backend
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
- `PG_DSN` (optional explicit DSN; otherwise pgx reads Aspire's PG variables, or defaults to the local development database on `54329`)
- `QUERY` / `CATEGORY` configure the managed Aspire resource's CLI arguments

Stop the shared AppHost before switching its mode or changing managed arguments.
See [shared Aspire workflows](../aspire-services/README.md) for credentials,
persistence, resource inspection and cleanup. The native Go example keeps the
library's Go 1.26 minimum; Go AppHost orchestration needs Go 1.26+. The sample's
OpenAI requests require explicit credentials and can incur charges.

## What it does

1. Generates embeddings for 3 hardcoded fake articles
2. Ensures a vector collection in Postgres
3. Upserts records and ensures vector/metadata indexes
4. Embeds your query and returns the top 3 nearest articles
