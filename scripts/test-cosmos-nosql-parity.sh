#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=cosmos-nosql -f internal/parity/catalog/Dockerfile .)
image=mcr.microsoft.com/cosmosdb/linux/azure-cosmos-emulator@sha256:db53613dae19d0a3d5ad50688d14aa6e957af082dafea56b52e1bd3a6b3f1406
server=$(docker run -d "$image" --protocol https)
trap 'docker rm -fv "$server" >/dev/null' EXIT
for ((attempt=1; attempt<=90; attempt++)); do
  if docker exec "$server" curl -fsS http://localhost:8080/ready >/dev/null 2>&1; then
    # Public development key for the local emulator.
    docker run --rm --network "container:$server" \
      -e COSMOS_EMULATOR_KEY='C2y6yDjf5/R+ob0N8A7Cgv30VRDJIWEHLM+4QDU5DE2nQ9nDuVTqobD4b8mGGyPMbIZnqyMsEcaGQy67XIw/Jw==' \
      "$runner" -test.timeout=2m
    exit 0
  fi
  sleep 1
done
docker logs --tail 50 "$server"
exit 1
