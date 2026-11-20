#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=mongodb -f internal/parity/catalog/Dockerfile .)
docker run --rm --network none --entrypoint dotnet "$runner" /app/Reference.dll probe parity
image=mongodb/mongodb-atlas-local:8.0@sha256:a58ea2c65424952e63beb56ebebb32a31f3ac3b13eb23c0b6e834c94388cec97
server=$(docker run -d --hostname atlas "$image")
trap 'docker rm -fv "$server" >/dev/null' EXIT
for ((attempt=1; attempt<=120; attempt++)); do
  if [[ $(docker inspect --format '{{.State.Health.Status}}' "$server") == healthy ]]; then
    docker run --rm --network "container:$server" "$runner"
    exit 0
  fi
  sleep 1
done
docker logs --tail 100 "$server"
exit 1
