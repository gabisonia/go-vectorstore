# Documentation retrieval pilot

This is a separate Go 1.25+ application pinned to the **published v0.5.0** module,
with no local `replace`. It turns this repository's Markdown docs into a small
semantic search workload and writes a reproducible JSON report. It uses SQLite
with packed vector projections and exact cosine search, plus the local
[all-minilm:22m embedding model](https://ollama.com/library/all-minilm:22m)
(approximately 46 MB). No chat model, agent framework, GPU or paid API is needed.
The vector store provides persistent typed chunks, automatic batched embeddings,
incremental writes, concurrent retrieval and reopening after restore.

See the [first measured results](results/README.md) for a completed fresh/reuse
run, per-query judgments, observed latency and a documented retrieval miss.

## Run

Use the existing [shared Aspire AppHost](../aspire-services/README.md), Aspire CLI
17.0.0, a current security-patched Go 1.26+ toolchain and Docker. The pilot itself
remains a Go 1.25+ consumer; only the AppHost requires Go 1.26+. From the repository
root:

```sh
VECTORSTORE_SAMPLE=docs-retrieval-pilot PILOT_REVISION="$(git rev-parse HEAD)" \
  aspire run --apphost samples/aspire-services/apphost.go
```

Run with a clean checkout when recording a revision. Otherwise append an explicit
dirty label to `PILOT_REVISION`. The report also includes hashes of the actual
corpus and query file. Select one mode per shared AppHost run; stop it before
switching modes. For concurrent checkouts, use distinct AppHost profile ports and
`PILOT_MODEL_VOLUME`/`PILOT_OUTPUT` values; see the shared host's operating notes.

Aspire manages three resources:

| Resource | Purpose |
| --- | --- |
| `pilot-ollama` | Pinned local Ollama, health check, dynamic endpoint, two CPUs/1 GiB and a separate persistent model cache |
| `pilot-models` | Downloads only `all-minilm:22m` if missing, then exits successfully |
| `docs-retrieval-pilot` | Host Go application; waits for model setup, receives the Ollama endpoint and runs the complete drill |

The model cache defaults to `vectorstore-docs-pilot_models`; the agent's chat model
and index volumes are not used. Embedding requests explicitly use two CPU threads,
recorded in the identity/report. No database services are started in this mode.

The first run embeds roughly a few thousand small chunks, depending on the
checkout. Allow several minutes on a CPU. Aspire's model setup needs internet
access; the Go program performs inference at the injected local Ollama endpoint.
Keep that service and model tag unchanged during a run.

In the dashboard, model setup should be `Exited` and the Go pilot `Finished`, both
with exit code zero. In another terminal at the repo
root, inspect logs or verify successful completion:

```sh
aspire logs docs-retrieval-pilot --apphost samples/aspire-services/apphost.go --tail 20
python3 scripts/wait-aspire-exit.py --apphost samples/aspire-services/apphost.go \
  --resource docs-retrieval-pilot --timeout 600

# Rerun with the same inputs: stored chunks are reused; only queries are embedded.
aspire resource docs-retrieval-pilot restart --apphost samples/aspire-services/apphost.go
```

Set `PILOT_CONCURRENCY=8 PILOT_REQUESTS_PER_WORKER=100` when launching the AppHost
for a larger load. Optional `PILOT_CORPUS`, `PILOT_QUERIES`, `PILOT_OUTPUT`,
`PILOT_EMBED_MODEL`, `PILOT_EMBED_DIMENSIONS` and `PILOT_MODEL_VOLUME` override the
inputs/model/cache. Relative file paths resolve from the pilot module directory.
An existing compatible Ollama service can also be used directly: from this
directory, run `go run . -ollama <origin>`; `go run . -help` lists native flags.

Default output is the ignored `samples/docs-retrieval-pilot/data/pilot` directory
on the host, available after the Aspire resource exits:

- `index.sqlite`: persistent typed chunks and the embedding/configuration identity.
- `backup.sqlite`: verified offline copy after all search workers and handles close.
- `restored.sqlite`: independent copy, reopened to verify records/vectors and rankings.
- `report.json`: configuration, environment, hashes, ingestion reuse, quality,
  successful search latency/throughput, failures, Go heap snapshots and restore evidence.

Only a report with `Completed: true`, `Stage: "complete"` and no error establishes
that this attempt finished. The CLI exits nonzero on validation, embedding, load,
copy or verification failures. Once the output lock is acquired it replaces an
earlier report with an incomplete attempt before indexing. Failures before lock
acquisition leave existing output alone. Preserve the exit status with your report.

## Evaluation contract

The corpus is every `.md` file recursively under `../../docs`. Symlinks are
rejected. Limits are 500 files, 512 KiB per file, 8 MiB total and 10,000 chunks;
all corpus/query validation happens before index changes. Chunking uses fixed
400-rune windows with 80-rune overlap, including Markdown/code, without title
prefixes or text cleanup. IDs are relative file paths plus rune offsets. This
deliberately simple baseline makes ingestion repeatable; it can split sentences.
Ollama receives `num_ctx=512` and `truncate=false`. Oversized/token-heavy input
fails visibly; select a smaller window in a new output directory if necessary.

[queries.json](queries.json) contains ten manually authored questions with
document-level judgments reviewed against the source docs: grade 2 is a direct
answer, grade 1 is related supporting material. These are a small, nonexhaustive
development set. Unjudged docs count as irrelevant; additional valid answers can
lower scores. They do not test whether a generated answer is correct. Keep the
questions and judgments fixed before comparing retrieval configurations, and use
a separately judged holdout set for tuning beyond this baseline.

Each query is embedded once. Search retrieves up to 100 chunks, then deduplicates
their sources in rank order to at most five documents. Fewer than five distinct
documents is possible if one source dominates the candidate list. The report
retains chunk IDs/scores and deduplicated source ranks. Macro-averaged metrics are:

- **Recall@k**: relevant documents retrieved divided by all judged relevant documents.
- **MRR@k**: reciprocal rank of the first relevant document, or zero.
- **nDCG@k**: discounted gain `(2^grade - 1) / log2(rank + 1)`, divided by the
  ideal ordering of judged documents at the same cutoff.

No quality threshold is imposed by CI; real semantic quality is measured with the
real model. Review individual query results and missed sources before deciding
whether the model/chunk strategy meets an application's needs. Changing the
candidate budget changes document coverage and search work, so record it.

## Load and memory boundaries

The load phase uses precomputed query vectors, four workers and 25 requests each
by default. Each request retrieves the same chunk budget as evaluation; corpus
reads, embedding generation, ingestion and restore are outside this timer.
Quality queries warm the store beforehand; reopened files do not imply cold OS
caches. SQLite uses one connection per store, so requests can queue. Each search
has a five-second deadline and the whole CLI has a ten-minute deadline.

Latency percentiles use nearest ranks of **successful** calls only; failures are
counted separately and make the attempt incomplete. Throughput is successful
calls divided by elapsed load wall time, including failed calls. The heap fields
are runtime snapshots before ingestion and after load, with cumulative allocation
and GC counters. They are **not peak memory, RSS or leak measurements**. The report
records Go version, OS, architecture, CPU count, library version, model digest and
Ollama version; record hardware, available RAM and competing workloads alongside
published measurements. This small, single-host, read-only load is not an SLA,
mixed write/read test, ANN recall test or a customer production validation.

## Identity, ingestion and restore

The database persists the Ollama tag's reported digest, dimensions, context/thread limits,
chunking version/windows and metric. Equal dimensions alone do not permit reuse.
A mismatch requires a new `-output` directory; no implicit re-embedding occurs.
The model tag is checked again at completion. These checks assume the operator
prevents external tag replacement while requests are in flight.

Unchanged chunks retain their vectors; stale IDs are deleted first to preserve
the chunk ceiling, then changed/new chunks are upserted in batches of 32 with at
most 16 inputs per embedding request. Each write batch is atomic, but the entire corpus sync is not
a single transaction. A failed attempt can leave partial progress, repaired by
the next successful run. This CLI owns its database; do not serve users from it
while ingestion, backup or restore runs.

The restore drill closes every index handle, refuses nonempty source or destination
WAL files, copies the database, syncs and hashes the destination, then copies and
reopens it with existing collection/identity metadata. It compares a sorted digest
of **every stored field and vector**, record count, and every evaluation query's
ranked chunk IDs and exact scores using the original query vectors. No embedding
calls are used to verify restoration. This checks a quiesced local file backup;
it does not establish live backup safety, power-loss recovery, remote backup
durability, disaster recovery objectives or filesystem-corruption tolerance.
See [production backup guidance](../../docs/production.md#sqlite-files-recovery-and-backups).

The output must be empty or marked as owned by this pilot. A `.pilot-lock` file
prevents two CLI processes from using it. After an unclean process termination,
remove that lock **only after confirming no pilot process is running**. Never
point this sample at application databases or replace files beneath an open store.

## Verification and cleanup

From the repository root, stop the selected AppHost and its persistent resources
while preserving model/data volumes:

```sh
aspire stop --apphost samples/aspire-services/apphost.go --force
```

The shared host's `--volumes` option also deletes its named volumes; use it only
when deliberately discarding those caches/databases. No global cleanup is needed.
Run the pilot's isolated checks from its module directory:

```sh
cd samples/docs-retrieval-pilot
# No Docker, model downloads or network service required.
go test -race -timeout=2m ./...
go vet ./...
go mod verify

# Optional: remove this pilot's generated databases and reports.
rm -r data/pilot
```

Tests use local HTTP fixtures and real SQLite for ingestion, unchanged-vector
reuse, updates/deletions, identity refusals, rankings and full backup/reopen.
They also check metric math, Unicode windows, invalid judgments, ownership/WAL
guards and embedding count/dimension/zero-vector/cancellation errors. Required CI
tests this isolated module on Go 1.25/current and scans its dependencies; weekly
scans include it. Root `go test ./...` does not traverse nested modules.
`bash scripts/verify-aspire.sh` also restores/builds/vets the shared AppHost without
starting services or downloading models.
