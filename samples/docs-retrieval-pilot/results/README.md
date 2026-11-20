# First measured documentation pilot

Both runs completed on 2026-11-20 using source revision
`4ffcec7f88bd9967d9837b845cfc060c8e7ac41f` and published go-vectorstore v0.5.0.
[summary.json](summary.json) contains the report fields for both runs, omitting
only the 100 raw chunk ranks per query to keep this checked-in evidence readable.
The CLI's generated `report.json` includes those ranks and scores. Corpus, query,
database-copy and full canonical-record SHA256 values remain in the extracts.

The host was an Apple M3 Pro, 36 GiB RAM, macOS 26.6.2, Go 1.27.4, arm64,
11 reported CPUs. SQLite and the CLI ran on the host. Ollama 0.40.16 ran in
Docker Desktop's approximately 7.65 GiB Linux VM, with this service limited to
two CPUs/1 GiB and requests using two threads. Other user services were running;
these are observations on a shared development machine, not isolated hardware
benchmarks. No other tests were run alongside the measured load phases.

The fixed model was `all-minilm:22m`, 384 dimensions, reported manifest digest
`1b226e2802dbb772b5fc32a58f103ca1804ef7501331012de126ab22f67475ef`.
The reports contain the exact digest observed at runtime; compare it when
reproducing, since model tags can change.

From the sample directory with its Compose service/model ready:

```sh
GOTOOLCHAIN=go1.27.4 go run . -ollama http://127.0.0.1:11435 -revision 4ffcec7f88bd9967d9837b845cfc060c8e7ac41f -output data/patched
# Preserve the first report before the next command replaces it.
cp data/patched/report.json data/fresh-report.json
GOTOOLCHAIN=go1.27.4 go run . -ollama http://127.0.0.1:11435 -revision 4ffcec7f88bd9967d9837b845cfc060c8e7ac41f -output data/patched
```

Use an empty owned output for the first command and the stated source revision
to reproduce these inputs. Both runs used 25 documents, 1,307 chunks, ten queries,
400-rune windows/80-rune overlap, cosine search, 100 chunk candidates and document
cutoff five. Configuration durations in JSON are integer nanoseconds.

| Observation | Fresh index | Unchanged-corpus rerun |
| --- | ---: | ---: |
| Chunks embedded / reused | 1,307 / 0 | 0 / 1,307 |
| Ingestion seconds | 15.938 | 0.117 |
| Mean Recall@5 | 0.900 | 0.900 |
| MRR@5 | 0.725 | 0.725 |
| nDCG@5 | 0.753 | 0.753 |
| Searches / failures, four workers | 100 / 0 | 100 / 0 |
| Successful searches per second | 108.74 | 118.01 |
| p50 / p95 search milliseconds | 27.08 / 72.85 | 26.22 / 75.20 |
| Go heap after load, MiB | 5.20 | 3.21 |
| Offline restore seconds, including record/ranking verification | 0.378 | 0.360 |
| Restored records and identical evaluation rankings | 1,307 / yes | 1,307 / yes |

Each backup was 13,414,400 bytes. Every canonical field/vector digest matched
across the two runs and each restore; unchanged ingestion did not replace stored
vectors. Latency variation between these short runs is visible; do not use a
single p95 value as a service objective. The approximately 1 GiB cumulative Go
allocation counters include all work since process startup, not just retained
heap or the timed load. A larger workload would need allocation profiles and
longer measurements before choosing a capacity target.

The `model-identity` question failed to retrieve its judged `production.md` answer
within the top five source documents. Migration and typed-filter questions found
their direct answers at ranks four and two. The fixed questions/judgments were
not changed after observing these results. This is a useful quality gap to carry
into a future holdout evaluation of heading-aware chunks or a stronger embedding
model. Ten development questions, warm exact scans and an offline file drill do
not establish customer workload quality or production recovery objectives.

An earlier exploratory ingestion was interrupted before measurement because
Ollama's default eleven threads saturated the VM. The final runs start from a
fresh index with the explicit two-thread setting; the interrupted attempt is not
included in the table or report extracts.

Final race tests, vet and the consumer vulnerability scan passed with Go 1.27.4.
The older system Go 1.26.1 was flagged for standard-library advisories during
verification; its exploratory measurements were replaced by these patched-toolchain runs.
