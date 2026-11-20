#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
file=internal/parity/catalog/Dockerfile
probe=$(docker build -q --target reference --build-arg provider=elasticsearch -f "$file" .)
docker run --rm --network none "$probe" dotnet /app/Reference.dll
runner=$(docker build -q --build-arg provider=elasticsearch --build-arg reference_dir=elasticsearch/vendor -f "$file" .)
image=docker.elastic.co/elasticsearch/elasticsearch:8.19.9@sha256:83f5129128b82b21fc9630c54312ad7d016d0129db138a25c2ef91d21ed00004
server=$(docker run -d -e discovery.type=single-node -e xpack.security.enabled=false \
  -e ES_JAVA_OPTS='-Xms512m -Xmx512m' --mount type=volume,destination=/usr/share/elasticsearch/data "$image")
trap 'docker rm -fv "$server" >/dev/null' EXIT
docker run --rm --network "container:$server" --entrypoint curl "$runner" \
  -fsS --retry 60 --retry-delay 1 --retry-all-errors 'http://localhost:9200/_cluster/health?wait_for_status=yellow'
docker run --rm --network "container:$server" "$runner"
