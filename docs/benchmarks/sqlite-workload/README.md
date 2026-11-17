# SQLite workload evidence

The [generated report](report.md) compares canonical, packed-Go and native-L1
execution at 10k/100k records, 128/768 dimensions, top 10/100, and 1%/10% data-filter
eligibility. It includes a separate all-tied corpus, inserts/upserts, explicit
rebuilds, Go allocations and main/WAL/SHM observations. All 360 timed result pages
passed independent reference checks.

The measurements exposed a real write-plan defect: SQLite selected a collection-only
primary-key scan during per-record projection cleanup. Requiring the existing
collection/key index removes that repeated scan. Formats, registrations, exact
ranking and transaction semantics are unchanged. Real plan/atomicity/conformance
tests cover the fix; no timing threshold is used as a test assertion.

- [random.jsonl](random.jsonl): complete four-fixture random matrix, 288 timed pages.
- [ties.jsonl](ties.jsonl): complete tied-vector profile, 72 timed pages.
- [summary.csv](summary.csv): medians, minima/maxima, sample counts and allocation counters.
- [diagnostic-before-partial.jsonl](diagnostic-before-partial.jsonl): deliberately
  interrupted diagnostic from the earlier source revision. Only two fully emitted
  fixture records are available; no completion marker exists. It is excluded from
  the final comparison matrix and labeled separately in the report.
- [manifest.json](manifest.json): artifact/generator hashes and provenance.

The fixed binary was built from clean revision
`b38b1eafd7b181a3fcea5e61a1764e9af38fd235`; the diagnostic used
`96cdace22192d2e961c3631858f00836a607fc4e`. Both used Go 1.26.1,
modernc SQLite v1.59.0/libc v1.75.7, Apple M3 Pro, 36 GiB RAM,
darwin/arm64 and GOMAXPROCS=11. Source-defined generation uses one deterministic
seed. Warm values are medians of three calls; reopened values are single
observations, with open time separate. Case order/cache/runtime history is retained,
OS caches were not flushed, and all outliers remain in raw data. These runs do not
establish cold-disk performance, tail latency, RSS, capacity limits or concurrent
service throughput. The final random run took about 28 minutes, sequentially.

Use the [harness instructions](../../../samples/sqlite-workload/README.md) to
reproduce workloads. To reproduce the recorded binary, build that exact revision
with Go 1.26.1. The final random invocation used `-timeout 90m`; the tied invocation
used `-timeout 20m`. The defaults for other recorded flags were explicitly
`-payload-bytes 4096 -batch 256 -samples 3`.

Regenerate and validate the summaries from the current repository root into new
temporary output files:

```sh
python3 samples/sqlite-workload/summarize.py docs/benchmarks/sqlite-workload/random.jsonl --ties docs/benchmarks/sqlite-workload/ties.jsonl --diagnostic docs/benchmarks/sqlite-workload/diagnostic-before-partial.jsonl --output /tmp/sqlite-workload-report.md --csv /tmp/sqlite-workload-summary.csv
python3 -B samples/sqlite-workload/check_report.py docs/benchmarks/sqlite-workload/random.jsonl --manifest docs/benchmarks/sqlite-workload/manifest.json
```

The generated report links raw basenames relative to this directory; temporary
renderings contain the same tables. The standard-library-only validator rejects
incomplete final matrices, missing samples and cross-path digest mismatches.
Small random/tied profiles and the write-plan proof run in normal and CGo-free
Linux/macOS/Windows CI; the full matrix is opt-in.
