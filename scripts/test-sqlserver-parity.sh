#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
runner=$(docker build -q --build-arg provider=sqlserver -f internal/parity/catalog/Dockerfile .)
image=mcr.microsoft.com/mssql/server:2025-latest@sha256:05644dad1266ca3d445d1e8f6d2ebb9a6a03df9a39376e0f7b48917356dca9d9
password="Sql-$(openssl rand -hex 20)"
server=$(docker run -d --platform linux/amd64 -e ACCEPT_EULA=Y -e MSSQL_PID=Developer \
  -e MSSQL_SA_PASSWORD="$password" --mount type=volume,destination=/var/opt/mssql "$image")
trap 'docker rm -fv "$server" >/dev/null' EXIT
sqlcmd() {
  docker exec -e SQLCMDPASSWORD="$password" "$server" /opt/mssql-tools18/bin/sqlcmd \
    -S localhost -U sa -C -b -l 2 -Q "$1"
}
for ((i=0; i<90; i++)); do
  if sqlcmd 'SELECT 1' >/dev/null 2>&1; then break; fi
  if [[ $(docker inspect -f '{{.State.Running}}' "$server") != true ]]; then
    docker logs --tail 30 "$server" >&2
    exit 1
  fi
  sleep 2
done
sqlcmd 'CREATE DATABASE parity'
docker run --rm --network "container:$server" \
  -e PARITY_SQL_URL="sqlserver://sa:$password@localhost:1433?database=parity&encrypt=true&TrustServerCertificate=true" \
  -e REFERENCE_SQL_DSN="Server=localhost;Database=parity;User ID=sa;Password=$password;Encrypt=True;TrustServerCertificate=True" "$runner"
