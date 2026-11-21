#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d)
containers=()
cleanup() {
  for id in "${containers[@]:-}"; do
    if [[ -n "$id" ]]; then docker rm -fv "$id" >/dev/null; fi
  done
  rm -rf "$work"
}
trap cleanup EXIT
export GOWORK=off
producer_commit=545df9b8974c88e086c2f599e33a6703a01c1033
mkdir "$work/release"
git archive "$producer_commit" | tar -x -C "$work/release"
cp internal/upgradefixture/main.go "$work/release/fixture.go"
(cd "$work/release" && go build -mod=readonly -o "$work/producer" fixture.go)
go build -mod=readonly -o "$work/reader" internal/upgradefixture/main.go
for layout in sqlite sqlite-projected sqlitevec; do
  "$work/producer" write "$layout" "$work/$layout.db"
  cp "$work/$layout.db" "$work/restored-$layout.db"
  "$work/reader" verify "$layout" "$work/restored-$layout.db"
done

create_server() {
  server=$(docker create -p "127.0.0.1::$port" "${args[@]}")
  containers+=("$server")
}
start() {
  port=$1
  shift
  args=("$@")
  create_server
  docker start "$server" >/dev/null
  endpoint=$(docker port "$server" "$port/tcp")
}
restore() {
  local data=$1
  docker stop "$server" >/dev/null
  docker cp -a "$server:$data/." - > "$work/backup.tar"
  create_server
  docker cp -a - "$server:$data" < "$work/backup.tar"
  docker start "$server" >/dev/null
  endpoint=$(docker port "$server" "$port/tcp")
}
wait_for() {
  for ((i=0;i<60;i++)); do if "$@" >/dev/null 2>&1; then return; fi; sleep 1; done
  docker logs --tail 30 "$server" >&2
  return 1
}
start 5432 -e POSTGRES_PASSWORD=fixture pgvector/pgvector:pg16
wait_for docker exec "$server" pg_isready -h localhost -U postgres
"$work/producer" write postgres "postgres://postgres:fixture@$endpoint/postgres?sslmode=disable"
restore /var/lib/postgresql/data
wait_for docker exec "$server" pg_isready -h localhost -U postgres
"$work/reader" verify postgres "postgres://postgres:fixture@$endpoint/postgres?sslmode=disable"
start 6379 redis:8.2
wait_for docker exec "$server" redis-cli ping
layouts=(snapshot hash json hash-native json-native json-text)
for layout in "${layouts[@]}"; do "$work/producer" write "$layout" "$endpoint"; done
docker exec "$server" redis-cli SAVE
restore /data
wait_for docker exec "$server" redis-cli ping
for layout in "${layouts[@]}"; do "$work/reader" verify "$layout" "$endpoint"; done
start 6333 --mount type=volume,destination=/qdrant/storage qdrant/qdrant:v1.20.2
wait_for curl -fsS "http://$endpoint/readyz"
"$work/producer" write qdrant "http://$endpoint"
restore /qdrant/storage
wait_for curl -fsS "http://$endpoint/readyz"
"$work/reader" verify qdrant "http://$endpoint"
echo "Verified all 11 v0.6.0 storage configurations from restored copies"
