#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=weaviate -f internal/parity/catalog/Dockerfile .)
image=cr.weaviate.io/semitechnologies/weaviate:1.40.7@sha256:1373d227ef808c95601b79be75d5e613c04c5ad33a1a2e91ed91a5e6c2748144
server=$(docker run -d -e AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED=true \
  -e AUTOSCHEMA_ENABLED=false -e DEFAULT_VECTORIZER_MODULE=none -e CLUSTER_HOSTNAME=node1 -e PERSISTENCE_DATA_PATH=/data \
  --mount type=volume,destination=/data "$image")
trap 'docker rm -fv "$server" >/dev/null' EXIT
docker run --rm --network "container:$server" --entrypoint curl "$runner" \
  -fsS --retry 40 --retry-delay 1 --retry-connrefused http://localhost:8080/v1/.well-known/ready
docker run --rm --network "container:$server" "$runner"
