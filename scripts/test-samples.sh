#!/usr/bin/env bash
# Run from any directory. All paths stay within this invocation's temp directory.
set -euo pipefail
cd "$(dirname "$0")/.."
sample_tmp="$(mktemp -d)"
trap 'rm -rf "$sample_tmp"' EXIT

go run ./samples/model-store
go run ./samples/input-embeddings
go run ./samples/output-embeddings
(
  cd samples/typed-fields
  go run ../../cmd/vectordata-gen -type Article -check
)
go run ./samples/typed-fields
go run ./samples/typed-fields -sqlite "$sample_tmp/fields.db"
go run ./samples/typed-fields -sqlite "$sample_tmp/fields.db"
go run ./samples/native-keys
go run ./samples/model-store -sqlite "$sample_tmp/models.db"
go run ./samples/sqlite-store -db "$sample_tmp/documents.db"
go run ./samples/sqlite-store -db "$sample_tmp/documents.db"
go run ./samples/sqlite-store -db "$sample_tmp/documents.db" -native
go run ./samples/sqlite-store -db "$sample_tmp/documents.db" -native
go run ./samples/sqlite-store -db "$sample_tmp/documents.db" -rebuild-indexes
go run ./samples/sqlite-store -db "$sample_tmp/documents.db" -vector-projections
go run ./samples/sqlite-store -db "$sample_tmp/documents.db" -rebuild-vectors
go run ./samples/sqlite-store -db "$sample_tmp/documents.db" -rebuild-vectors
go run ./samples/sqlite-store -db "$sample_tmp/documents.db" -native -rebuild-vectors
go run ./samples/sqlitevec-store -db "$sample_tmp/sqlitevec.db"
go run ./samples/sqlitevec-store -db "$sample_tmp/sqlitevec.db"
go run ./samples/sqlitevec-store -db "$sample_tmp/sqlitevec.db" -rebuild-vectors
go run ./samples/sqlite-workload -records 200 -dimensions 8 -tops 3,7 -samples 1 -payload-bytes 64 -quiet -output "$sample_tmp/workload.jsonl"
python3 samples/sqlite-workload/summarize.py "$sample_tmp/workload.jsonl" --output "$sample_tmp/workload-summary.md" --csv "$sample_tmp/workload-summary.csv"
python3 -B samples/sqlite-workload/check_report.py "$sample_tmp/workload.jsonl"
python3 -B samples/sqlite-workload/check_report.py docs/benchmarks/sqlite-workload/random.jsonl --manifest docs/benchmarks/sqlite-workload/manifest.json
go run ./samples/typed-search -category architecture
go run ./samples/typed-rag -sqlite "$sample_tmp/rag.db"
for recipe in single-record named-vectors scalar-query key-filters squared-l2 iterators embedded-iterators metadata capabilities; do
  go run ./samples/recipes -recipe "$recipe"
done
