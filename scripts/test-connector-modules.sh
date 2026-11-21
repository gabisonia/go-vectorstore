#!/usr/bin/env bash
# Resolve candidate nested tags from a disposable Git repository with the same
# module paths. The core dependency resolves normally from its published tag.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOWORK=off
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
repo=github.com/gabisonia/go-vectorstore
root=$PWD
mkdir "$tmp/app"
if [[ ${1:-} == --published ]]; then
  ver=v0.1.0
else
  for name in azuresearch pinecone; do
    (cd "connectors/$name" && go test -mod=readonly -race ./... && go vet -tags=referenceparity ./... && go test -mod=readonly -tags=referenceparity -c -o "$tmp/$name.test" ./internal/parity)
  done
  mkdir "$tmp/source"
  cp -R connectors "$tmp/source/"
  git -C "$tmp/source" init -q
  git -C "$tmp/source" add connectors
  git -C "$tmp/source" -c user.name=fixture -c user.email=fixture@example.invalid commit -qm fixture
  ver=v0.1.0-test.g$(git -C "$tmp/source" rev-parse --short=12 HEAD)
  for name in azuresearch pinecone; do git -C "$tmp/source" tag "connectors/$name/$ver"; done
  export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0="url.file://$tmp/source.insteadOf" GIT_CONFIG_VALUE_0="https://$repo"
  export GOPRIVATE="$repo/connectors/*" GONOPROXY="$repo/connectors/*" GONOSUMDB="$repo/connectors/*"
fi
cd "$tmp/app"
go mod init example.invalid/consumer
cat > main.go <<'GO'
package main
import (
 "github.com/gabisonia/go-vectorstore/connectors/azuresearch"
 "github.com/gabisonia/go-vectorstore/connectors/pinecone"
)
func main() {
 _, _ = azuresearch.AttachNative, pinecone.AttachNative
}
GO
go get "$repo/connectors/azuresearch@$ver" "$repo/connectors/pinecone@$ver"
go build -mod=readonly .
# Also compile against the candidate root before it receives a public tag.
unset GOWORK
go work init . "$root"
go build .
