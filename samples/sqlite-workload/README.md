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
/tmp/sqlite-workload -records 10000,100000 -dimensions 128,768 -tops 10,100 -samples 3 -machine 'describe CPU and RAM' -output random.jsonl
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

Longer runs and additional datasets are needed for general capacity or throughput
claims. Native scalar L1 still scans all vectors, and complete ties can return all
vectors to Go. See the [precision proof and limits](../../docs/sqlitevec.md) and
[parity plan](../../docs/parity-plan.md).
