#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
work=$(mktemp -d)
server=
cleanup() {
  if [[ -n "$server" ]]; then docker rm -fv "$server" >/dev/null; fi
  rm -rf "$work"
}
trap cleanup EXIT
unset HANDBOOK_POSTGRES_DSN
go build -o "$work/handbook" ./samples/handbook
cp -R samples/handbook/pages "$work/pages"
args=(-pages "$work/pages")
case "${1:-sqlite}" in
  sqlite) args+=(-sqlite "$work/handbook.db");;
  postgres)
    server=$(docker run -d -p 127.0.0.1::5432 -e POSTGRES_PASSWORD=handbook pgvector/pgvector:pg16)
    port=$(docker port "$server" 5432/tcp)
    for ((i=0;i<60;i++)); do
      if docker exec "$server" pg_isready -h localhost -U postgres >/dev/null; then break; fi
      sleep 1
    done
    export HANDBOOK_POSTGRES_DSN="postgres://postgres:handbook@$port/postgres?sslmode=disable";;
  *) echo "usage: bash samples/handbook/replay.sh [sqlite|postgres]" >&2; exit 2;;
esac
handbook() { "$work/handbook" "${args[@]}" "$@"; }
query='How do I cancel a request at its deadline?'
expect_passage() {
  local result
  result=$(handbook query "$query")
  printf '%s\n' "$result"
  if [[ "$result" != "timeouts.md"$'\n'"$1" ]]; then echo 'Unexpected search result' >&2; exit 1; fi
}
before='When a request exceeds its deadline, cancel the context and page the API on-call.'
after='When a request exceeds its deadline, cancel the context and contact the platform rotation.'
printf 'Index the handbook and search for "%s"\n' "$query"
handbook index
expect_passage "$before"
echo 'Edit timeouts.md. The stored passage still has the previous text:'
printf '%s\n' "$after" > "$work/pages/timeouts.md"
expect_passage "$before"
echo 'Reindex and repeat the same search:'
handbook index
expect_passage "$after"
