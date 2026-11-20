#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=pinecone -f internal/parity/catalog/Dockerfile .)
image=ghcr.io/pinecone-io/pinecone-local:v1.0.0.rc0@sha256:0f944585830a30c37260ac6534c7c206280490947e1ddbb716474166b517b6c7
server=$(docker run -d --platform linux/amd64 -e PORT=5080 "$image")
trap 'docker rm -fv "$server" >/dev/null' EXIT
docker run --rm --network "container:$server" --entrypoint curl "$runner" \
  -fsS --retry 30 --retry-delay 1 --retry-all-errors http://localhost:5080/indexes >/dev/null
docker run --rm --network "container:$server" "$runner"
