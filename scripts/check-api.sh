#!/usr/bin/env bash
# Compare each public module with its own reviewed release tag.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOWORK=off
api_tmp="$(mktemp -d)"
trap 'rm -rf "$api_tmp"' EXIT
api_module=github.com/gabisonia/go-vectorstore
api_tool=v0.0.0-20261119141654-b15d094f3610
mkdir "$api_tmp/bin"
GOBIN="$api_tmp/bin" go install "golang.org/x/exp/cmd/apidiff@$api_tool"

export_api() (
  cd "$1"
  local module=$2 output=$3
  # A deleted or renamed nested go.mod must not resolve via the parent module.
  local actual
  actual=$(go list -m)
  if [[ $actual != "$module" ]]; then
    printf 'Expected module %s in %s, found %s\n' "$module" "$PWD" "$actual" >&2
    return 1
  fi
  "$api_tmp/bin/apidiff" -m -w "$output" "$module"
)

check_module() {
  local dir=$1 module=$2 tag=$3
  local baseline="$api_tmp/$tag"
  printf 'Checking %s against %s\n' "$module" "$tag"
  mkdir -p "$baseline"
  git archive "refs/tags/$tag" -- "$dir" | tar -x -C "$baseline"
  export_api "$baseline/$dir" "$module" "$api_tmp/old.export"
  export_api "$dir" "$module" "$api_tmp/new.export"
  # apidiff reports incompatibilities on stdout and exits successfully.
  "$api_tmp/bin/apidiff" -m -incompatible "$api_tmp/old.export" "$api_tmp/new.export" > "$api_tmp/report"
  diff -u /dev/null "$api_tmp/report"
}

check_module . "$api_module" v1.0.0
check_module connectors/azuresearch "$api_module/connectors/azuresearch" connectors/azuresearch/v0.1.0
check_module connectors/pinecone "$api_module/connectors/pinecone" connectors/pinecone/v0.1.0
