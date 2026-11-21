#!/usr/bin/env bash
# Protect every public package in the current published minor line. Tool deps
# stay outside the library module. Advance the baseline after a reviewed release.
set -euo pipefail
cd "$(dirname "$0")/.."
api_tmp="$(mktemp -d)"
trap 'rm -rf "$api_tmp"' EXIT
api_module=github.com/gabisonia/go-vectorstore
api_baseline=v0.6.0
api_tool=v0.0.0-20261119141654-b15d094f3610
mkdir -p "$api_tmp/baseline" "$api_tmp/bin"
git archive "$api_baseline" | tar -x -C "$api_tmp/baseline"
GOBIN="$api_tmp/bin" go install "golang.org/x/exp/cmd/apidiff@$api_tool"
(
  cd "$api_tmp/baseline"
  "$api_tmp/bin/apidiff" -m -w "$api_tmp/old.export" "$api_module"
)
"$api_tmp/bin/apidiff" -m -w "$api_tmp/new.export" "$api_module"
# apidiff reports differences on stdout but does not fail for incompatibility.
"$api_tmp/bin/apidiff" -m -incompatible "$api_tmp/old.export" "$api_tmp/new.export" > "$api_tmp/report"
# Only this minor transition permits the two reviewed package relocations.
: > "$api_tmp/expected"
if [[ $(cat VERSION) == 0.7.0 && $api_baseline == v0.6.0 ]]; then
  printf -- '- package %s/stores/%s: removed\n' "$api_module" azuresearch "$api_module" pinecone > "$api_tmp/expected"
fi
diff -u "$api_tmp/expected" "$api_tmp/report"
printf 'API differences match the reviewed transition from %s.\n' "$api_baseline"
