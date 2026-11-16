#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
smoke_tmp="$(mktemp -d)"
trap 'rm -rf "$smoke_tmp"' EXIT

docker compose run --build --rm -T agent ask 'Can SQLite do hybrid search? What should I use instead? Cite the documentation.' 2>&1 | tee "$smoke_tmp/answer.log"
grep -Fq '[tool] search_docs' "$smoke_tmp/answer.log"
grep -Fq '[tool] read_section' "$smoke_tmp/answer.log"
grep -Fq 'Verified source IDs:' "$smoke_tmp/answer.log"

docker compose run --rm -T agent ingest 2>&1 | tee "$smoke_tmp/reopen.log"
grep -Fq '0 new/changed' "$smoke_tmp/reopen.log"

printf '%s\n' 'Does SQLite support hybrid search?' 'How should I back it up?' exit |
  docker compose run --rm -T agent chat 2>&1 | tee "$smoke_tmp/chat.log"
[[ "$(grep -Fc 'Verified source IDs:' "$smoke_tmp/chat.log")" -eq 2 ]]
printf 'Local model, tool calls, persistence and follow-up conversation passed.\n'
