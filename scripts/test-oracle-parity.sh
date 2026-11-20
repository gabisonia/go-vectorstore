#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=oracle -f internal/parity/catalog/Dockerfile .)
image=gvenzl/oracle-free:23-slim-faststart@sha256:cebd977833411a42170777d8e9ccf616a028a2bb137ae96498354ab6c7510268
password="Ora$(openssl rand -hex 12)"
server=$(docker run -d -e ORACLE_RANDOM_PASSWORD=yes -e APP_USER=parity -e APP_USER_PASSWORD="$password" "$image")
trap 'docker rm -fv "$server" >/dev/null' EXIT
for ((i=0; i<120; i++)); do
  if docker exec "$server" healthcheck.sh >/dev/null 2>&1; then
    docker run --rm --network "container:$server" \
      -e PARITY_ORACLE_URL="oracle://parity:$password@localhost:1521/FREEPDB1" \
      -e REFERENCE_ORACLE_DSN="User Id=parity;Password=$password;Data Source=localhost:1521/FREEPDB1" "$runner"
    exit 0
  fi
  if [[ $(docker inspect -f '{{.State.Running}}' "$server") != true ]]; then break; fi
  sleep 2
done
echo 'Oracle Free did not become ready' >&2
exit 1
