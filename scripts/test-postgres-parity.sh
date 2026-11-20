#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=postgres -f internal/parity/catalog/Dockerfile .)
# The runner shares the disposable server's loopback interface.
for version in 16 18; do
  server=$(docker run -d -e POSTGRES_PASSWORD=fixture pgvector/pgvector:pg"$version")
  trap 'docker rm -fv "$server" >/dev/null' EXIT
  for ((i=0; i<60; i++)); do
    if docker exec "$server" pg_isready -h 127.0.0.1 -U postgres >/dev/null; then break; fi
    sleep 1
  done
  docker run --rm --network "container:$server" \
    -e PARITY_PG_URL='postgres://postgres:fixture@localhost:5432/postgres?sslmode=disable' \
    -e REFERENCE_PG_DSN='Host=localhost;Database=postgres;Username=postgres;Password=fixture;SSL Mode=Disable' "$runner"
  docker rm -fv "$server" >/dev/null
  trap - EXIT
done
