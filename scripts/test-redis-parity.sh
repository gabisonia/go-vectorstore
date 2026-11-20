#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=redis -f internal/parity/catalog/Dockerfile .)
server=$(docker run -d redis:8.2)
trap 'docker rm -fv "$server" >/dev/null' EXIT
for ((i=0; i<30; i++)); do
  if docker exec "$server" redis-cli ping | grep -qx PONG; then break; fi
  sleep 1
done
docker run --rm --network "container:$server" "$runner"
