#!/usr/bin/env python3
"""Validate workload JSONL and reproduce Markdown/CSV summaries (stdlib only)."""
import argparse
import csv
import itertools
import json
from pathlib import Path
import statistics

MODES = ("canonical", "packed_go", "sqlitevec")


def load(path, diagnostic=False):
    events = [json.loads(line) for line in Path(path).read_text(encoding="utf-8").splitlines() if line.strip()]
    if not events or events[0].get("type") != "environment" or events[0].get("schema") != 1:
        raise ValueError(f"{path}: missing schema-1 environment")
    env = events[0]
    complete = events[-1].get("type") == "complete"
    if not complete and not diagnostic:
        raise ValueError(f"{path}: no final completion event")
    fixtures = events[1:-1] if complete else events[1:]
    if not fixtures:
        raise ValueError(f"{path}: no completed fixtures")
    if any(row.get("type") != "fixture" for row in fixtures):
        raise ValueError(f"{path}: unexpected event")
    config = env["config"]
    expected = set(itertools.product(config["records"], config["dimensions"]))
    actual = {(f["records"], f["dimensions"]) for f in fixtures}
    if len(actual) != len(fixtures) or not actual <= expected:
        raise ValueError(f"{path}: duplicate/unrequested fixtures")
    if complete and (actual != expected or events[-1]["fixtures"] != len(fixtures)):
        raise ValueError(f"{path}: completion does not cover the requested matrix")
    if complete:
        env["completed_utc"] = events[-1]["finished_utc"]
    for fixture in fixtures:
        if fixture["distribution"] != config["distribution"]:
            raise ValueError("distribution differs from environment")
        n = fixture["records"]
        for operation, count in (("load", n), ("upsert", min(n, 1024))):
            for mode in ("canonical", "projected"):
                m = fixture[operation][mode]
                if m["records"] != count or m["calls"] != (count + config["batch_records"] - 1) // config["batch_records"]:
                    raise ValueError(f"incomplete {operation}: {fixture['records']}/{fixture['dimensions']}")
        if fixture["rebuild"]["records"] != n or fixture["rebuild"]["calls"] != 1:
            raise ValueError("incomplete rebuild")
        identities, digests = set(), {}
        for query in fixture["queries"]:
            identity = (query["top"], query["eligible_percent"], query["mode"], query["cache"], query["sample"])
            if identity in identities:
                raise ValueError("duplicate query sample")
            identities.add(identity)
            eligible = query["eligible_percent"]
            count = n if eligible == 100 else ((n + 99) // 100 if eligible == 1 else n // 100 * 10 + min(n % 100, 10))
            if query["eligible_records"] != count or query["matches"] != min(query["top"], count):
                raise ValueError("unexpected eligibility/page count")
            if query["measurement"]["calls"] != 1 or query["measurement"]["records"] != query["matches"] or query["measurement"]["duration_ns"] < 0:
                raise ValueError("invalid query measurement")
            key = (query["top"], eligible)
            digest = query["page_sha256"]
            if len(digest) != 64 or key in digests and digests[key] != digest:
                raise ValueError("page digests differ across modes/cache conditions")
            digests[key] = digest
        expected_queries = {(top, eligible, mode, cache, sample)
                            for top, eligible, mode in itertools.product(config["tops"], (100, 1, 10), MODES)
                            for cache, samples in (("warm", config["warm_samples"]), ("reopened", 1))
                            for sample in range(1, samples + 1)}
        if identities != expected_queries:
            raise ValueError("query matrix is incomplete")
    return env, sorted(fixtures, key=lambda f: (f["records"], f["dimensions"])), complete


def summaries(fixtures):
    result = []
    for f in fixtures:
        groups = {}
        for q in f["queries"]:
            key = (q["top"], q["eligible_percent"], q["mode"], q["cache"])
            groups.setdefault(key, []).append(q)
        for (top, eligible, mode, cache), rows in sorted(groups.items()):
            times = [q["measurement"]["duration_ns"] / 1e6 for q in rows]
            result.append(dict(records=f["records"], dimensions=f["dimensions"], distribution=f["distribution"], top=top,
                               eligible_percent=eligible, mode=mode, cache=cache, samples=len(rows),
                               median_ms=statistics.median(times), min_ms=min(times), max_ms=max(times),
                               median_go_bytes=statistics.median(q["measurement"]["go_allocated_bytes"] for q in rows),
                               median_go_allocations=statistics.median(q["measurement"]["go_allocations"] for q in rows),
                               open_ms=statistics.median(q.get("open_ns", 0) / 1e6 for q in rows)))
    return result


def table(lines, headers, rows):
    lines.extend(["", "| " + " | ".join(headers) + " |", "| " + " | ".join("---" for _ in headers) + " |"])
    lines.extend("| " + " | ".join(map(str, row)) + " |" for row in rows)


def render(env, fixtures, rows, random_path, ties=None, diagnostic=None):
    c = env["config"]
    lines = ["# SQLite workload measurements", "", "Generated by `samples/sqlite-workload/summarize.py`; raw samples remain authoritative.", "",
             f"Environment: {c['machine_label'] or 'hardware label not provided'}; {env['os']}/{env['arch']}; {env['go']}; "
             f"{env['logical_cpus']} logical CPUs, GOMAXPROCS={env['gomaxprocs']}.", "",
             f"Binary source: `{env['vcs_revision']}` (modified tree: {env['vcs_modified']}). "
             f"Dependencies: " + ", ".join(f"`{k} {v}`" for k, v in env["dependencies"].items()) + ".", "",
             f"Raw complete matrix: [{Path(random_path).name}]({Path(random_path).name}). "
             f"{len(fixtures)} fixtures, {sum(len(f['queries']) for f in fixtures)} timed, independently verified pages.", "",
             f"Run window: `{env['started_utc']}` to `{env.get('completed_utc', 'incomplete')}`.", "",
             f"Body: {c['payload_bytes']} bytes; batch: {c['batch_records']} records; metric: L1. "
             f"Warm values are medians of {c['warm_samples']} calls. Reopened values are one first-query observation plus separately measured open time.", "",
             "Canonical uses an unprojected database; packed Go/native share a second file with identical canonical data. Mode order rotates. "
             "Generation, validation and requested GC are outside query timers. OS caches are untouched: reopened does not mean cold disk I/O.", "",
             "Three warm samples do not establish tail latency. This is one machine, one corpus seed and one pass; neighboring Go GC/scavenging and OS work can affect results. "
             "CSV summaries retain minima/maxima and raw JSONL retains every sample/outlier. Go allocation counters are not RSS or SQLite temporary-storage usage.", "",
             "Cases follow the configured top order (10 before 100 in the published matrix). Cache, storage and runtime history carry between cases; "
             "nonmonotonic timings do not establish that a larger requested page is intrinsically faster."]
    lookup = {(r["records"], r["dimensions"], r["top"], r["eligible_percent"], r["mode"], r["cache"]): r for r in rows}
    cells = lambda f, top, eligible, cache="warm": [lookup[(f["records"], f["dimensions"], top, eligible, mode, cache)] for mode in MODES]
    lines += ["", "## Unfiltered warm search", "", "Times are milliseconds. These paths still scan the selected vector population; no ANN index is used."]
    table(lines, ["Records", "Dimensions", "Top", "Canonical ms", "Packed Go ms", "Native L1 ms"],
          ([f["records"], f["dimensions"], top] + [f"{r['median_ms']:.2f}" for r in cells(f, top, 100)] for f in fixtures for top in c["tops"]))
    lines += ["", "## Unfiltered cumulative Go allocations", "", "MiB per search; lower Go allocations do not imply constant total memory. Native candidate selection materializes key/distance pairs in SQLite."]
    table(lines, ["Records", "Dimensions", "Top", "Canonical MiB", "Packed Go MiB", "Native L1 MiB"],
          ([f["records"], f["dimensions"], top] + [f"{r['median_go_bytes']/2**20:.2f}" for r in cells(f, top, 100)] for f in fixtures for top in c["tops"]))
    lines += ["", "## Filtered warm search", "", "Eligibility describes matching records, not records scanned. Every current filtered path scans and validates canonical payloads; packed/native unfiltered gains do not transfer to these filters."]
    table(lines, ["Records", "Dimensions", "Top", "Eligible %", "Canonical ms", "Packed Go ms", "Native handle ms"],
          ([f["records"], f["dimensions"], top, eligible] + [f"{r['median_ms']:.2f}" for r in cells(f, top, eligible)] for f in fixtures for top in c["tops"] for eligible in (1, 10)))
    lines += ["", "## First unfiltered query after reopen", "", "Each cell is query ms / open ms. OS caches are not flushed; these are not controlled cold-I/O measurements."]
    table(lines, ["Records", "Dimensions", "Top", "Canonical", "Packed Go", "Native L1"],
          ([f["records"], f["dimensions"], top] + [f"{r['median_ms']:.2f} / {r['open_ms']:.2f}" for r in cells(f, top, 100, "reopened")] for f in fixtures for top in c["tops"]))
    lines += ["", "## Durable writes and rebuild", "", "Load times sum timed batch API calls, alternating baseline/projected order. Upserts change vectors/payloads for the first min(records, 1024) keys. "
              "Projected Go and native handles share write code. Each rebuild is one explicit atomic replacement under the writer lock. These are single operation observations, not throughput distributions."]
    table(lines, ["Records", "Dimensions", "Canonical load s", "Projected load s", "Canonical upsert ms", "Projected upsert ms", "Rebuild s"],
          ([f["records"], f["dimensions"], f"{f['load']['canonical']['duration_ns']/1e9:.3f}", f"{f['load']['projected']['duration_ns']/1e9:.3f}",
            f"{f['upsert']['canonical']['duration_ns']/1e6:.2f}", f"{f['upsert']['projected']['duration_ns']/1e6:.2f}", f"{f['rebuild']['duration_ns']/1e9:.3f}"] for f in fixtures))
    lines += ["", "## Storage observations", "", "Main files below are measured after load and closing both writers. WAL is measured after rebuild while query handles remain open; it is not a peak. "
              "Raw records also include SHM, final-close durations and post-workload storage. No VACUUM, cache eviction or provider tuning was applied."]
    table(lines, ["Records", "Dimensions", "Canonical main MiB", "Projected main MiB", "Open rebuild WAL MiB"],
          ([f["records"], f["dimensions"], f"{f['storage_after_load']['canonical']['main_bytes']/2**20:.2f}", f"{f['storage_after_load']['projected']['main_bytes']/2**20:.2f}", f"{f['storage_after_rebuild_open']['wal_bytes']/2**20:.2f}"] for f in fixtures))
    variable = sorted((r for r in rows if r["cache"] == "warm" and r["median_ms"] and r["max_ms"] / r["median_ms"] >= 1.5), key=lambda r: r["max_ms"] / r["median_ms"], reverse=True)[:6]
    if variable:
        lines += ["", "## Largest observed warm-sample variation", "", "These samples were retained, not retried or removed. Three calls cannot estimate p95/p99."]
        table(lines, ["Records", "Dimensions", "Top", "Eligible %", "Mode", "Min ms", "Median ms", "Max ms"],
              ([r["records"], r["dimensions"], r["top"], r["eligible_percent"], r["mode"], f"{r['min_ms']:.2f}", f"{r['median_ms']:.2f}", f"{r['max_ms']:.2f}"] for r in variable))
    if ties:
        tie_env, tie_fixtures, tie_path = ties
        tie_rows = summaries(tie_fixtures)
        lines += ["", "## Complete-boundary ties", "", f"Raw complete profile: [{Path(tie_path).name}]({Path(tie_path).name}). All initial vectors are identical. "
                  "Native L1 must retain the entire boundary tie group, so every eligible vector can return to Go. Unique-distance allocation/latency results are not universal."]
        table(lines, ["Records", "Dimensions", "Top", "Mode", "Warm ms", "Go MiB/search"],
              ([r["records"], r["dimensions"], r["top"], r["mode"], f"{r['median_ms']:.2f}", f"{r['median_go_bytes']/2**20:.2f}"] for r in tie_rows if r["cache"] == "warm" and r["eligible_percent"] == 100))
    if diagnostic:
        before_env, before_fixtures, before_path, before_complete = diagnostic
        fixed = {(f["records"], f["dimensions"]): f for f in fixtures}
        lines += ["", "## Write-plan defect discovered during measurement", "", f"Diagnostic source `{before_env['vcs_revision']}`: [{Path(before_path).name}]({Path(before_path).name}). "
                  f"Completion marker present: {before_complete}. The run was deliberately interrupted during a later load; only its {len(before_fixtures)} fully emitted fixture records are diagnostic evidence. "
                  "This file is excluded from the final matrix above.", "",
                  "EXPLAIN selected a collection-only primary-key scan for per-record projection cleanup. The CPU profile was dominated by reads from that cleanup. "
                  "Requiring the existing collection/key index removes the repeated collection scan without changing storage layout or canonical semantics. "
                  "A real query-plan regression check guards both equality constraints. Before/after write/rebuild figures below are single observations on matching completed fixtures."]
        table(lines, ["Records", "Dimensions", "Before projected load s", "Fixed projected load s", "Before rebuild s", "Fixed rebuild s"],
              ([f["records"], f["dimensions"], f"{f['load']['projected']['duration_ns']/1e9:.3f}", f"{fixed[(f['records'],f['dimensions'])]['load']['projected']['duration_ns']/1e9:.3f}",
                f"{f['rebuild']['duration_ns']/1e9:.3f}", f"{fixed[(f['records'],f['dimensions'])]['rebuild']['duration_ns']/1e9:.3f}"] for f in before_fixtures if (f["records"], f["dimensions"]) in fixed))
    lines += ["", "## Interpretation", "", "Packed/native paths reduce full-payload decoding for unfiltered dense searches. Filtered searches keep canonical scan cost. "
              "Projections add storage and write work; native candidates use SQLite temporary storage and complete ties can eliminate candidate reduction. "
              "Choose settings using representative local workloads and longer isolated runs; this matrix establishes neither capacity limits nor tail latency, RSS, cold-disk behavior or concurrent-service throughput."]
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("random")
    parser.add_argument("--ties")
    parser.add_argument("--diagnostic")
    parser.add_argument("--output", required=True)
    parser.add_argument("--csv")
    args = parser.parse_args()
    env, fixtures, _ = load(args.random)
    if env["config"]["distribution"] != "random":
        raise ValueError("primary matrix must use the random distribution")
    rows = summaries(fixtures)
    ties = None
    if args.ties:
        tie_env, tie_fixtures, _ = load(args.ties)
        if tie_env["config"]["distribution"] != "ties":
            raise ValueError("ties input must use the tied distribution")
        for key in ("go", "os", "arch", "gomaxprocs", "vcs_revision", "dependencies"):
            if env[key] != tie_env[key]:
                raise ValueError(f"ties environment differs: {key}")
        ties = tie_env, tie_fixtures, args.ties
    diagnostic = None
    if args.diagnostic:
        before_env, before_fixtures, before_complete = load(args.diagnostic, diagnostic=True)
        for key in ("go", "os", "arch", "gomaxprocs", "dependencies"):
            if env[key] != before_env[key]:
                raise ValueError(f"diagnostic environment differs: {key}")
        for key in ("batch_records", "payload_bytes", "distribution"):
            if env["config"][key] != before_env["config"][key]:
                raise ValueError(f"diagnostic fixture differs: {key}")
        diagnostic = before_env, before_fixtures, args.diagnostic, before_complete
    paths = [Path(args.output)] + ([Path(args.csv)] if args.csv else [])
    if any(path.exists() for path in paths):
        raise FileExistsError("summary output must be new")
    text = render(env, fixtures, rows, args.random, ties, diagnostic)
    with paths[0].open("x", encoding="utf-8") as output:
        output.write(text)
    if args.csv:
        with Path(args.csv).open("x", newline="", encoding="utf-8") as output:
            writer = csv.DictWriter(output, fieldnames=list(rows[0]))
            writer.writeheader()
            writer.writerows(rows + (summaries(ties[1]) if ties else []))


if __name__ == "__main__":
    main()
