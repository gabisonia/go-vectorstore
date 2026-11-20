#!/usr/bin/env bash
# The fixture uses fixed collection names only inside this disposable server.
set -euo pipefail
cd "$(dirname "$0")/.."
project=internal/parity/qdrant/Reference.csproj
dotnet restore "$project" --locked-mode
dotnet build "$project" --no-restore
container=$(docker run -d --network bridge -p 127.0.0.1::6333 -p 127.0.0.1::6334 qdrant/qdrant:v1.20.2)
trap 'docker rm -fv "$container" >/dev/null' EXIT
export PARITY_HTTP_URL="http://$(docker port "$container" 6333/tcp)"
grpc=$(docker port "$container" 6334/tcp)
export PARITY_GRPC_PORT="${grpc##*:}"
curl --retry 30 --retry-delay 1 --retry-connrefused -fsS "$PARITY_HTTP_URL/readyz" >/dev/null
go test -count=1 -race -tags=qdrantparity -timeout=3m -v -run '^TestNative(DotNetParity|IndexedRecall)$' ./stores/qdrant
