#!/usr/bin/env bash
# Fixed fixture names are private to this disposable server.
set -euo pipefail
cd "$(dirname "$0")/.."
image=$(docker build -q -f internal/parity/qdrant/Dockerfile .)
server=$(docker run -d --network bridge qdrant/qdrant:v1.20.2)
trap 'docker rm -fv "$server" >/dev/null' EXIT
docker run --rm --network "container:$server" --entrypoint curl "$image" \
  --retry 30 --retry-delay 1 --retry-connrefused -fsS http://localhost:6333/readyz
docker run --rm --network "container:$server" \
  -e PARITY_HTTP_URL=http://localhost:6333 -e PARITY_GRPC_PORT=6334 \
  "$image" -test.run '^TestNative(DotNetParity|IndexedRecall|RankedGuides)$'
