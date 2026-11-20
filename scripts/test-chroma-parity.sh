#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=chroma -f internal/parity/catalog/Dockerfile .)
image=chromadb/chroma:1.5.9@sha256:1e0b73a187a28757c572acba508c46f48c9e8b0acaf5c20e6d95cdedce1acdf6
server=$(docker run -d -e RAYON_NUM_THREADS=1 --mount type=volume,destination=/data "$image")
trap 'docker rm -fv "$server" >/dev/null' EXIT
docker run --rm --network "container:$server" --entrypoint curl "$runner" \
  -fsS --retry 40 --retry-delay 1 --retry-connrefused http://localhost:8000/api/v2/heartbeat
docker run --rm --network "container:$server" "$runner"
