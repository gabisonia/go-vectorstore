#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
image=ghcr.io/documentdb/documentdb/documentdb-local:pg17-1.0.0@sha256:5c3df9c6aeb50b649fe7b296e83f59751131e4283f2e655980064d2ac7ebfdac
server=$(docker run -d --rm -p 127.0.0.1::10260 -e USERNAME=parity -e PASSWORD=LocalFixture123 -e DOCUMENTDB_USAGE_TELEMETRY=false "$image")
trap 'docker stop "$server" >/dev/null' EXIT
port=$(docker port "$server" 10260 | sed 's/.*://')
uri="mongodb://parity:LocalFixture123@127.0.0.1:${port}/?tls=true&tlsInsecure=true&directConnection=true&retryWrites=false&authMechanism=SCRAM-SHA-256"
for ((attempt=1; attempt<=90; attempt++)); do
  if docker exec "$server" mongosh 'mongodb://parity:LocalFixture123@localhost:10260/?tls=true&tlsInsecure=true&directConnection=true&retryWrites=false&authMechanism=SCRAM-SHA-256' --quiet --eval 'db.runCommand({ping:1}).ok' >/dev/null 2>&1; then
    DOCUMENTDB_RECOVERY_URI="$uri" go test -count=1 -race -v -timeout=3m ./internal/parity/documentdbrecovery
    exit 0
  fi
  sleep 1
done
docker logs --tail 50 "$server"
exit 1
