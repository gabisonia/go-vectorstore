# Shared Aspire services and sample workflows

Aspire CLI **17.0.0**, **Go 1.26+** and Docker are the prerequisites. This isolated
Go AppHost owns the sample databases, startup dependencies, local models and
application resources. The library remains Go 1.26+ with its own dependencies.
Run the commands below from the repository root.

## Development databases

```sh
aspire start --apphost samples/aspire-services/apphost.go
aspire wait postgres --apphost samples/aspire-services/apphost.go
aspire wait redis --apphost samples/aspire-services/apphost.go
aspire wait qdrant --apphost samples/aspire-services/apphost.go
```

The default `services` mode starts:

| Resource | Configuration |
| --- | --- |
| PostgreSQL | pgvector/pgvector:pg16, `localhost:54329`, database `vectorstore_test`, local demo user/password `postgres` |
| Redis | redis:8.2 with JSON and Search, `localhost:63799`, local plaintext/no password, AOF/everysec, noeviction |
| Qdrant | qdrant/qdrant:v1.20.2, standalone, `http://localhost:63339` |

These containers and their volumes persist across ordinary AppHost stops.
`DEV_POSTGRES_PORT`, `DEV_REDIS_PORT` and `DEV_QDRANT_PORT` override host ports. The dashboard uses
19180–19182; change `aspire.config.json` if needed. It can run beside the separate
[vector search UI](../aspire-vector-search/README.md), which uses other resources.
After changing a persistent resource's configuration, use its Restart action
(for example, `aspire resource redis restart --apphost samples/aspire-services/apphost.go`)
to recreate its container with the new settings while keeping its volume.

Native examples keep their existing commands:

```sh
go run ./samples/model-store -dsn 'postgres://postgres:postgres@localhost:54329/vectorstore_test?sslmode=disable'
go run ./samples/redis-store -addr localhost:63799
go run ./samples/qdrant-store
```

The fixed PostgreSQL credentials are local development defaults, not production
configuration. The AppHost models the password as a secret parameter. Integration
tests continue to provision their own disposable databases using Testcontainers.

## OpenAI sample modes

Start one selected mode at a time. Stop the shared AppHost before switching modes.
The managed application gets standard PG variables from Aspire, so its database
port can be dynamically allocated. No database URL/password is embedded in its
application arguments.

```sh
export OPENAI_API_KEY=your_key_here
VECTORSTORE_SAMPLE=semantic-search QUERY='go cancellation patterns' CATEGORY=backend \
  aspire run --apphost samples/aspire-services/apphost.go
```

After the database is healthy, deliberately start **semantic-search** in the
dashboard or from another terminal:

```sh
aspire resource semantic-search start --apphost samples/aspire-services/apphost.go
aspire logs semantic-search --apphost samples/aspire-services/apphost.go
```

For the story RAG example:

```sh
VECTORSTORE_SAMPLE=ragrimosa QUERY='How did Lacrimosa and Rex build trust?' TOP_K=4 \
  aspire run --apphost samples/aspire-services/apphost.go
```

Then start **ragrimosa** in the dashboard or with `aspire resource ragrimosa start`.
Both applications use **explicit start**, so starting the AppHost alone never
invokes OpenAI. Set the key before launching, and opt into starting the sample;
OpenAI requests can incur charges. `OPENAI_BASE_URL`, `OPENAI_EMBEDDING_MODEL` and
`OPENAI_CHAT_MODEL` retain their existing meanings. An explicitly provided native
`PG_DSN` still overrides PG variables outside the managed resource.

## Local documentation agent

```sh
VECTORSTORE_SAMPLE=local-doc-agent \
  aspire run --apphost samples/aspire-services/apphost.go
```

This mode starts pinned local Ollama, waits for health, runs model initialization,
then builds/runs the existing agent Dockerfile after successful setup. The named
`vector-doc-agent_models` and `vector-doc-agent_index` volumes preserve the
sample's existing downloads and SQLite index. Models are large and CPU inference
can take minutes. No cloud key is needed.

Set `AGENT_MODEL_VOLUME` and `AGENT_INDEX_VOLUME` to distinct Docker volume names
when isolating a local-agent run from another checkout. Reuse those names between
ask/ingest/chat phases to keep its cache and index. Other AppHost ports still need
to be distinct when running multiple checkouts concurrently.

Configure `AGENT_COMMAND=ask|ingest|chat` (default ask) and `QUERY` for a one-shot
question. With **chat**, Aspire's resource terminal provides interactive input:

```sh
VECTORSTORE_SAMPLE=local-doc-agent AGENT_COMMAND=chat \
  aspire run --apphost samples/aspire-services/apphost.go
# In a second terminal, or use the dashboard's Terminals view:
aspire terminal attach agent --apphost samples/aspire-services/apphost.go
```

`Ctrl+B D` detaches the CLI terminal without stopping the agent. `exit` ends chat.
For model settings, corpus/index behavior and the full real-model smoke test, see
[local-doc-agent](../local-doc-agent/README.md).

## Documentation retrieval pilot

```sh
VECTORSTORE_SAMPLE=docs-retrieval-pilot PILOT_REVISION="$(git rev-parse HEAD)" \
  aspire run --apphost samples/aspire-services/apphost.go
```

This mode starts only `pilot-ollama`, a `pilot-models` initialization container and
the `docs-retrieval-pilot` Go application. Aspire owns readiness, the injected
Ollama endpoint, model setup, application arguments and logs. It downloads only
the small MiniLM embedding model, with a separate cache volume and two CPUs/1 GiB
for Ollama; it does not start the agent's chat model or development databases.
The pilot exits after ingestion, quality/load evaluation and verified offline
SQLite restore. Its report and databases remain on the host under
`samples/docs-retrieval-pilot/data/pilot`.

Use `aspire resource docs-retrieval-pilot restart` with this AppHost to rerun and
check embedding reuse. `PILOT_CONCURRENCY` and `PILOT_REQUESTS_PER_WORKER` customize
load when launching; `PILOT_MODEL_VOLUME` and `PILOT_OUTPUT` isolate parallel
checkouts, whose AppHost profile ports must also differ. See the
[pilot instructions](../docs-retrieval-pilot/README.md) for completion checks,
all overrides and measurement boundaries.

## Inspect and stop

```sh
aspire describe --apphost samples/aspire-services/apphost.go
aspire describe --apphost samples/aspire-services/apphost.go --format mermaid
aspire logs --apphost samples/aspire-services/apphost.go
aspire stop --apphost samples/aspire-services/apphost.go
```

Normal stop retains persistent databases, Ollama, networks and volumes. Use
`--force` to stop/remove this AppHost's persistent resources while preserving
volumes. Adding `--volumes` explicitly deletes its named data/model volumes,
including existing agent downloads/indexes. No global Docker cleanup is needed.
Do not run concurrent agents against the same SQLite index or two copies of this
AppHost against its fixed development ports/volumes.

If Docker reports that all predefined address pools have been subnetted, allocate
a dedicated non-overlapping network or free an unused network you own. Do not
prune unrelated resources. The current workspace uses a pre-created persistent
network for this AppHost; ordinary restarts reuse it.

## Verification

```sh
bash scripts/verify-aspire.sh
```

This generates ignored SDK code, builds/vets both Go AppHosts and runs the vector
API tests. It requires no containers, models or credentials. Root tests do not
traverse the isolated AppHost modules. The real local-agent smoke test exercises
Ollama, model setup, persistence, tool calls and terminal conversation separately.
