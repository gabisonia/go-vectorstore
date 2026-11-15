#!/usr/bin/env bash
# Run bounded, deterministic-seed fuzz campaigns with a small worker count.
set -euo pipefail
cd "$(dirname "$0")/.."
fuzz_time="${1:-3s}"
if [[ ! "$fuzz_time" =~ ^[1-9][0-9]*(ms|s|m)$ ]]; then
  printf 'Usage: test-fuzz.sh POSITIVE_DURATION (for example 3s or 1m)\n' >&2
  exit 2
fi
for target in FuzzDecimal FuzzFloat16 FuzzSparseDistance FuzzStructCodec FuzzModelFilter; do
  go test -run '^$' -fuzz "^${target}$" -fuzztime "$fuzz_time" -parallel=2 -timeout=5m ./vectordata
done
go test -run '^$' -fuzz '^FuzzStoredRecord$' -fuzztime "$fuzz_time" -parallel=2 -timeout=5m ./stores/sqlite
go test -run '^$' -fuzz '^FuzzBoundFilterOperands$' -fuzztime "$fuzz_time" -parallel=2 -timeout=5m ./stores/postgres/filtersql
