# Reproducible SQLite workload measurements

This tool owns and removes disposable fixture directories. It compares unprojected
canonical storage with a projected file read through packed-Go and sqlite-vec
handles. It needs no server, C compiler, API key or new dependency.

Run a small check first:

```sh
go run ./samples/sqlite-workload -records 200 -dimensions 8 -tops 3,7 -samples 1 -payload-bytes 64 -output smoke.jsonl
```

The report filename must be new; existing files are never overwritten. Progress
goes to stderr (`-quiet` suppresses it). The JSONL report emits an environment
record, one record per completed fixture, then a final `complete` event. A missing
completion event means the run failed or was canceled; partial runs must not be
presented as a complete matrix. Temporary databases are removed after each fixture,
including ordinary errors/cancellation. The report contains no local fixture paths.

The broader matrix is deliberately opt-in and can take tens of minutes and several
GB of temporary disk space. Use a local filesystem and run it sequentially without
other benchmarks/tests competing for CPU or disk:

```sh
go build -o /tmp/sqlite-workload ./samples/sqlite-workload
/tmp/sqlite-workload -records 10000,100000 -dimensions 128,768 -tops 10,100 -samples 3 -timeout 90m -machine 'describe CPU and RAM' -output random.jsonl
/tmp/sqlite-workload -records 10000 -dimensions 128 -tops 10,100 -samples 3 -distribution ties -machine 'describe CPU and RAM' -output ties.jsonl
```

On Windows, build an `.exe` in a temporary location instead. `-workdir` selects an
existing parent for the tool's own temporary directories; it never removes that
parent. `-timeout` defaults to 45 minutes for the complete invocation. Record the
machine label, actual toolchain/module versions, source revision and modified-tree
flag from the environment event when publishing results. Fixed source-defined
SplitMix64 values generate moderate float32 components; the all-tied profile uses
identical vectors. Query generation, reference ranking and validation are untimed.

The default body is a 4 KiB string; batch size is 256. All records have one dense
L1 vector and a Group value from 0 to 99. The tool measures:

- Durable insert API calls for identical batches in baseline/projected files,
  alternating their order. Both Go/native projected handles share the same write
  implementation, so a separate native-write result is not invented.
- Unfiltered, 1%-eligible and 10%-eligible searches at both requested top values.
  Data-filtered searches currently scan canonical payloads in all three modes;
  selectivity describes eligible results, not records scanned.
- Warm samples after an untimed warm-up, rotating mode order, with GC requested
  before each timer. One first-query sample after reopening each provider handle
  includes a separate open duration. OS caches stay untouched; reopened is not
  cold disk I/O. No cache eviction or system-wide tuning is performed.
- Vector/payload upserts for the first min(records, 1024) keys in both files,
  followed by a query that must observe a newly exact-matching vector.
- An explicit full rebuild while query handles remain idle, followed by result
  verification. Rebuild time includes canonical decoding/validation and atomic
  projection replacement.
- Main/WAL/SHM bytes after load/close, after rebuild while open, and after final
  close/checkpoint. Open-file sizes are observations, not peak storage or RSS.

Every measured page checks all keys, distance bits, scores, payloads and omitted
vectors against an independent bounded sorted-insertion reference generated from
the input. A SHA-256 digest records keys/distances/scores for cross-path comparison.
All generation uses bounded batches, and the reference retains only the largest
requested page. Timing/allocation counters enclose API calls only. Cumulative Go
allocations exclude retained heap/RSS and SQLite temporary-storage requirements.

Regenerate summaries with the standard-library-only validator/renderer:

```sh
python3 samples/sqlite-workload/summarize.py random.jsonl --ties ties.jsonl --output report.md --csv summary.csv
```

It requires complete fixture/sample coverage, matching page digests across all
modes/cache conditions, and a final completion event. Summary outputs must be new
files. CSV retains sample counts, minima/maxima and allocation counters. An optional
`--diagnostic` input may contain completed fixture records from an interrupted
earlier run; those are labeled incomplete diagnostic evidence and excluded from
the final comparison matrix. Small CI runs exercise rejection of incomplete,
missing-sample and mismatched-digest reports. Keep all measured outliers.
Cases follow the requested top order, and cache/runtime history carries between
them. Nonmonotonic timings do not prove that larger pages are intrinsically faster.

Longer runs and additional datasets are needed for general capacity or throughput
claims. Native scalar L1 still scans all vectors, and complete ties can return all
vectors to Go. See the [precision proof and limits](../../docs/sqlitevec.md) and
[parity plan](../../docs/parity-plan.md).

## Concurrent WAL workload

Run the measured profile with independent reader and writer handles on a fresh
database for each layout and load condition:

```sh
SQLITE_WAL_PROFILE=measured go test -count=1 -v -run '^TestConcurrentWAL$' ./samples/sqlite-workload
```

Each successful run prints four JSON objects marked `WAL_RESULT`. Require the
final Go test PASS because fresh reads verify the writes after metrics are
printed. The default test runs eight rounds. The opt-in `large` profile runs
200 rounds against 10,000 stable 128-dimensional vectors and 200 mutable rows.

The measured fixture has 1,024 stable 32-dimensional vectors and 100 mutable
rows. Four seeded queries rotate across 100 rounds with top 10 and L1 distance.
The mutable vectors lie beyond the stable candidates before and after every
update. This keeps the exact reference valid for every WAL snapshot and lets
packed search use its unfiltered path. Each mixed round launches one search
and one single-row upsert and waits for both. Overlap counts intersecting API
call intervals. Latency percentiles use nearest ranks over API calls. Rates
include round coordination and page verification. SQLite uses WAL, FULL
synchronization and a zero SQLite busy timeout. Errors count final API outcomes.

| Layout | Load | Search p50 / p95 (ms) | Write p50 / p95 (ms) | Searches/s |
| --- | --- | ---: | ---: | ---: |
| Canonical | Read only | 8.28 / 9.24 | — | 119 |
| Canonical | Mixed | 8.34 / 8.77 | 0.21 / 0.34 | 120 |
| Packed Go | Read only | 0.87 / 1.04 | — | 1,110 |
| Packed Go | Mixed | 1.17 / 1.42 | 0.26 / 0.54 | 847 |

Both mixed runs overlapped all 100 pairs, verified every search and final
write, and reported zero lock errors, cancellations and unexpected failures.
These single-run timings describe a paced writer on a small Darwin arm64
fixture. See the [machine-readable measurements](concurrent-results.json).
