# Local documentation agent

A small working agent that researches local Markdown documents. It combines
Microsoft Agent Framework Go v0.2.0, go-vectorstore v0.4.1, SQLite, and Ollama.
There is no cloud account or paid API key in the default configuration.

This is an isolated Go 1.26+ module. The main library keeps its own dependencies
and Go minimum. The sample is available in this repository checkout; run commands
from this directory.

## Run with Docker Compose

Docker with Compose v2 is the only requirement. The default models are
[Qwen3 4B Instruct](https://ollama.com/library/qwen3:4b-instruct) (approximately
2.5 GB of weights) and [all-MiniLM 22M](https://ollama.com/library/all-minilm)
(approximately 46 MB). Allow several GB of disk and enough available RAM for the
models plus Docker. The CPU-only configuration was tested on an 8 GB Docker
VM; existing workloads consume part of that budget. It requires no GPU. CPU
responses can take minutes; the default deadline per turn is five minutes.

From the repository root:

```sh
cd samples/local-doc-agent
docker compose run --build --rm agent
```

Compose starts Ollama, waits for health, downloads missing models into a persistent
volume, builds the Go application, indexes the bundled documents, and asks:

> Can SQLite do hybrid search? What should I use instead? Cite the documentation.

The terminal shows actual tool invocations, then the model's answer and checked
source IDs. Expected factual content: SQLite lacks hybrid search and PostgreSQL
with pgvector provides it. Wording/tool order vary; the application rejects an
answer that does not inspect a section or includes unverified citation IDs.

Ask another question or start a conversation:

```sh
docker compose run --rm -T agent ask 'How should I back up SQLite safely?'
docker compose run --rm agent chat
```

Try follow-ups such as “What are its limits?” or “How does PostgreSQL differ?”
Conversation history is managed by Agent Framework in the current chat process;
it is not saved across CLI restarts. Type `reset` to start a fresh conversation
when changing topics or nearing the small model's context budget. The document
index does persist.

`docker compose up --build` also runs the default one-shot question. The agent
container exits after answering, while Ollama remains available. Prefer the
`compose run` commands for interactive use. After the first setup, existing model
weights are reused without pulling their manifests again; inference is local.
Initial image/model downloads require internet access.

## Why both libraries are useful here

A basic retrieve-then-generate function would not require an agent framework.
This demo instead exposes two typed function tools:

1. `search_docs(query)` embeds the model-chosen query and searches go-vectorstore.
   It returns section IDs, titles and short discovery snippets.
2. `read_section(section_id)` reads an indexed section through go-vectorstore.
   The model chooses sections to inspect and may refine its search before answering.

Agent Framework owns schemas, dispatch, automatic model/tool iterations and session
history. The application requires an initial search and caps tool iterations; later
tool choices are model decisions. If a small model tries to answer before reading,
the application makes at most three corrective framework rounds requiring search
or source inspection; the model still chooses its query/section arguments. Failed
inspection/citations remain errors. go-vectorstore owns persistence, embeddings
on changed records, retrieval and section filtering. Its SQLite file is the data store.
A small HTTP adapter connects local Ollama embeddings to vectordata.Embedder; the
framework's OpenAI-compatible chat adapter connects to the same local server.

The `[tool]` trace makes this observable. Citation checks establish that IDs refer
to sections read during the current turn. They do **not** prove every claim is
supported, guarantee answer quality, or make document instructions safe. Review
answers against sources; a small model can make mistakes or fail to use tools.

## Documents and persistence

Edit/add Markdown under `docs/`. Each invocation reads the corpus, splits headings
into sections and small chunks, upserts new/changed records, and removes stale
records. Unchanged documents are not embedded again. `ingest` updates just the index:

```sh
docker compose run --rm -T agent ingest
```

This parser handles ordinary Markdown headings/fenced code, not every Markdown
extension. It skips symlinks and accepts up to 100 files, 128 KiB per file, 2 MiB
total and 4,000 chunks. Small chunks fit all-MiniLM's limited context; embedding
truncation is disabled so oversized/token-dense inputs fail explicitly.
`read_section` caps text returned to the model at 3,000 runes and reports truncation.

The `index` volume contains only this sample's `demo_chunks` and `demo_metadata`
collections. Index synchronization is a single-process demo workflow, not an atomic
multi-process corpus deployment. Finish an invocation before starting another.
Do not mount a production index here. No arbitrary filesystem-reading tool is exposed.

The index records the embedding model name and dimensions and rejects a change
with the same database. Model tags can change upstream; this is not a model digest
registry. Use a fresh index when changing embedding weights/configuration.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `CHAT_MODEL` | `qwen3:4b-instruct` | Local chat model pulled by setup |
| `MODEL_THREADS` | `4` | CPU threads for the local chat-model alias |
| `EMBED_MODEL` | `all-minilm:22m` | Local embedding model |
| `EMBED_DIMENSIONS` | `384` | Must match embedding model output |
| `DEMO_SUBNET` | `10.254.249.0/24` | Dedicated Compose subnet; override if it overlaps your VPN/Docker networks |
| `OLLAMA_URL` | `http://ollama:11434` in Compose | Local inference endpoint |
| `DOCS_DIR` | `/docs` in Compose | Mounted Markdown corpus |
| `DATABASE_PATH` | `/data/documents.db` in Compose | Persistent SQLite file |
| `REQUEST_TIMEOUT` | `5m` | Deadline for indexing and each question |

Compose creates the local `doc-agent-chat` alias from CHAT_MODEL with a bounded
context/token budget and MODEL_THREADS (default four). The alias shares its source
model weights. Lower thread counts can improve CPU performance on oversubscribed
laptops.

Model/subnet overrides can be supplied as Compose environment variables:

```sh
CHAT_MODEL=qwen3:1.7b docker compose run --build --rm agent
```

The smaller alternative uses less memory but tool use/citations may be less
reliable; it is not the default validated model. No host inference port is exposed.
Ollama cloud use is disabled in the default service. For native accelerated Ollama,
run Go on the host with OLLAMA_URL pointing to that local installation instead;
Docker-on-macOS uses CPU here. See [Ollama Docker guidance](https://docs.ollama.com/docker).

Stop the sample while retaining downloads/index, or explicitly reset its volumes:

```sh
docker compose down
# Deletes this Compose project's model downloads AND indexed documents:
docker compose down -v
```

## Verification

```sh
go test -race -timeout=2m ./...
go vet ./...
bash smoke.sh
```

Go checks need Go 1.26+ but no models, network service, or API key. Tests run the
real Agent Framework tool loop against a local HTTP stand-in and real temporary
SQLite storage, including reopens, document updates/removals, model identity,
source-ID validation, embedding failures and cancellation.

`smoke.sh` uses the real Compose services/models and can download several GB on
its first run. It checks actual search/read tool calls, cited output, persistent
index reuse and a follow-up conversation. It leaves the cached models/index
available for further exploration. This sample uses a preview framework with
pinned versions; it is a local learning project, not a production service.
