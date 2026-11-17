#!/usr/bin/env bash
# Run all fuzz targets with fixed seeds and bounded time or call budgets.
set -euo pipefail
cd "$(dirname "$0")/.."
# Count budgets avoid the Go 1.25 fuzz-timeout shutdown race (golang/go#75804).
# Duration budgets remain available for longer campaigns. Real failures are fatal.
fuzz_budget="${1:-50000x}"
if [[ ! "$fuzz_budget" =~ ^[1-9][0-9]*(ms|s|m|x)$ ]]; then
  printf 'Usage: test-fuzz.sh POSITIVE_DURATION_OR_CALL_COUNT (for example 3s, 1m or 50000x)\n' >&2
  exit 2
fi
for target in FuzzDecimal FuzzFloat16 FuzzSparseDistance FuzzStructCodec FuzzModelFilter; do
  go test -run '^$' -fuzz "^${target}$" -fuzztime "$fuzz_budget" -parallel=2 -timeout=5m ./vectordata
done
go test -run '^$' -fuzz '^FuzzStoredRecord$' -fuzztime "$fuzz_budget" -parallel=2 -timeout=5m ./stores/sqlite
go test -run '^$' -fuzz '^FuzzBoundFilterOperands$' -fuzztime "$fuzz_budget" -parallel=2 -timeout=5m ./stores/postgres/filtersql
