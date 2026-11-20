#!/usr/bin/env bash
# Review the public module against the last published baseline. Tool deps stay
# outside the library module. Update the reviewed report deliberately per release.
set -euo pipefail
cd "$(dirname "$0")/.."
api_tmp="$(mktemp -d)"
trap 'rm -rf "$api_tmp"' EXIT
api_module=github.com/gabisonia/go-vectorstore
api_tool=v0.0.0-20261119141654-b15d094f3610
mkdir -p "$api_tmp/baseline" "$api_tmp/bin"
git archive v0.4.1 | tar -x -C "$api_tmp/baseline"
GOBIN="$api_tmp/bin" go install "golang.org/x/exp/cmd/apidiff@$api_tool"
(
  cd "$api_tmp/baseline"
  "$api_tmp/bin/apidiff" -m -w "$api_tmp/old.export" "$api_module"
)
"$api_tmp/bin/apidiff" -m -w "$api_tmp/new.export" "$api_module"
# apidiff reports differences on stdout but does not fail for incompatibility.
"$api_tmp/bin/apidiff" -m -incompatible "$api_tmp/old.export" "$api_tmp/new.export" > "$api_tmp/report"
diff -u scripts/api-v0.4.1-incompatible.txt "$api_tmp/report"
cat "$api_tmp/report"
printf 'Public API matches the reviewed v0.4.1-to-v0.5.0 compatibility report.\n'
