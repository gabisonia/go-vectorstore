#!/usr/bin/env bash
# Run from any directory. All paths stay within this invocation's temp directory.
set -euo pipefail
cd "$(dirname "$0")/.."
sample_tmp="$(mktemp -d)"
trap 'rm -rf "$sample_tmp"' EXIT

go run ./samples/model-store
go run ./samples/input-embeddings
go run ./samples/native-keys
go run ./samples/model-store -sqlite "$sample_tmp/models.db"
go run ./samples/sqlite-store -db "$sample_tmp/documents.db"
go run ./samples/sqlite-store -db "$sample_tmp/documents.db"
go run ./samples/typed-search -category architecture
go run ./samples/typed-rag -sqlite "$sample_tmp/rag.db"
for recipe in named-vectors squared-l2 iterators embedded-iterators capabilities; do
  go run ./samples/recipes -recipe "$recipe"
done
