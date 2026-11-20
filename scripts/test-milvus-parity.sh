#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=milvus -f internal/parity/catalog/Dockerfile .)
docker run --rm --network none --entrypoint dotnet "$runner" /app/Reference.dll probe parity
image=milvusdb/milvus:v2.6.27@sha256:396499822afda0f4cc95190df016370a73e6c977fd9a4e71db23d5d21ab178da
config=$(mktemp -d)
server=
cleanup() {
  if [[ -n "$server" ]]; then docker rm -fv "$server" >/dev/null; fi
  rm -f "$config/etcd.yaml"
  rmdir "$config"
}
trap cleanup EXIT
cat > "$config/etcd.yaml" <<'YAML'
listen-client-urls: http://127.0.0.1:2379
advertise-client-urls: http://127.0.0.1:2379
YAML
server=$(docker run -d -e ETCD_USE_EMBED=true -e ETCD_DATA_DIR=/var/lib/milvus/etcd \
  -e ETCD_CONFIG_PATH=/milvus/configs/embedEtcd.yaml -e COMMON_STORAGETYPE=local -e DEPLOY_MODE=STANDALONE \
  --mount "type=bind,source=$config/etcd.yaml,target=/milvus/configs/embedEtcd.yaml,readonly" \
  --mount type=volume,destination=/var/lib/milvus "$image" milvus run standalone)
if ! docker run --rm --network "container:$server" --entrypoint curl "$runner" \
  -fsS --retry 90 --retry-delay 1 --retry-all-errors --max-time 5 http://localhost:9091/healthz; then
  docker logs --tail 120 "$server"
  exit 1
fi
docker run --rm --network "container:$server" "$runner"
