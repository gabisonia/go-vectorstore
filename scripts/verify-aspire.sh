#!/usr/bin/env bash
# No services, models or cloud credentials are needed for these checks.
set -euo pipefail
cd "$(dirname "$0")/.."
aspire --version
verify_tmp="$(mktemp -d)"
trap 'rm -rf "$verify_tmp"' EXIT

for sample in aspire-services aspire-vector-search; do
  aspire restore --apphost "samples/$sample/apphost.go" --non-interactive
  (
    cd "samples/$sample"
    go build -o "$verify_tmp/$sample" .
    go vet ./...
  )
done
(
  cd samples/aspire-vector-search/api
  go test -race -timeout=2m ./...
  go vet ./...
)
