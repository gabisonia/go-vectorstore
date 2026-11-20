#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
file=internal/parity/catalog/Dockerfile
probe=$(docker build -q --target reference --build-arg provider=couchbase -f "$file" .)
runner=$(docker build -q --build-arg provider=couchbase --build-arg reference_dir=couchbase/vendor -f "$file" .)
image=couchbase:enterprise-8.0.0@sha256:fbf87a1b7233f3b36794722be267d8b5e3dd6b7de9187e283632a2c153ff82d3
server=$(docker run -d "$image")
trap 'docker rm -fv "$server" >/dev/null' EXIT
admin=Administrator:parity-fixture
api() { docker exec -i "$server" curl -fsS -u "$admin" "$@"; }
api --retry 60 --retry-all-errors --retry-delay 1 http://localhost:8091/pools >/dev/null
docker exec "$server" couchbase-cli cluster-init -c 127.0.0.1 --cluster-username Administrator \
  --cluster-password parity-fixture --services data,index,query,fts --cluster-ramsize 512 \
  --cluster-index-ramsize 256 --cluster-fts-ramsize 256 --index-storage-setting default
docker exec "$server" couchbase-cli bucket-create -c 127.0.0.1 -u Administrator -p parity-fixture \
  --bucket parity --bucket-type couchbase --bucket-ramsize 128 --bucket-replica 0 --wait
api -d name=docs http://localhost:8091/pools/default/buckets/parity/scopes
api -d name=articles http://localhost:8091/pools/default/buckets/parity/scopes/docs/collections
api --retry 45 --retry-all-errors --retry-delay 1 -X PUT -H 'Content-Type: application/json' \
  --data-binary @- http://localhost:8094/api/bucket/parity/scope/docs/index/vectors <internal/parity/catalog/couchbase/index.json
docker run --rm --network "container:$server" "$probe" dotnet /app/Reference.dll
docker run --rm --network "container:$server" "$runner"
