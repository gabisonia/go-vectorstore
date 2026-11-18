#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
apphost="$PWD/samples/aspire-services/apphost.go"
smoke_tmp="$(mktemp -d)"
started=0
cleanup() {
  if [[ "$started" == 1 ]]; then aspire stop --apphost "$apphost" --non-interactive || true; fi
  rm -rf "$smoke_tmp"
}
trap cleanup EXIT

# Never replace a user's already-running services or conversation.
aspire ps --format Json --non-interactive > "$smoke_tmp/running.json"
python3 - "$apphost" "$smoke_tmp/running.json" <<'PY'
import json, os, sys
data=json.load(open(sys.argv[2]))
rows=data if isinstance(data,list) else data.get("appHosts",[])
if any(os.path.realpath(r.get("appHostPath",""))==sys.argv[1] for r in rows):
    raise SystemExit("The shared sample AppHost is already running. Stop it before this smoke test.")
PY

start_phase() {
  local phase="$1"
  started=1
  VECTORSTORE_SAMPLE=local-doc-agent AGENT_COMMAND="$phase" \
    aspire start --apphost "$apphost" --format Json --non-interactive > "$smoke_tmp/$phase-start.json"
}
stop_phase() { aspire stop --apphost "$apphost" --non-interactive; started=0; }
logs() { aspire logs agent --apphost "$apphost" --tail 5000 --non-interactive; }
wait_exit() {
  if ! python3 scripts/wait-aspire-exit.py --apphost "$apphost" --resource agent --timeout 1200; then
    logs
    aspire logs models --apphost "$apphost" --tail 30 --non-interactive
    return 1
  fi
}

start_phase ask
wait_exit
logs | tee "$smoke_tmp/answer.log"
grep -Fq '[tool] search_docs' "$smoke_tmp/answer.log"
grep -Fq '[tool] read_section' "$smoke_tmp/answer.log"
grep -Fq 'Verified source IDs:' "$smoke_tmp/answer.log"

stop_phase

start_phase ingest
wait_exit
logs | tee "$smoke_tmp/reopen.log"
grep -Fq '0 new/changed' "$smoke_tmp/reopen.log"

stop_phase

start_phase chat
aspire wait agent --apphost "$apphost" --status up --timeout 1200 --non-interactive
cat > "$smoke_tmp/chat.tape" <<'TAPE'
Set TypingSpeed 0
Set WaitTimeout 10m
Type "Does SQLite support hybrid search?"
Enter
Wait+Screen /Verified source IDs:/
Wait+Line /You>/
Type "How should I back it up?"
Enter
Type "exit"
Enter
TAPE
# A freshly attached container terminal may not replay its initial prompt.
# Submit the first line directly. The agent processes both turns sequentially;
# successful process exit and two citation checks below prove both completed.
aspire terminal tape play agent --apphost "$apphost" --tape-file "$smoke_tmp/chat.tape" --timeout 1200 --non-interactive > "$smoke_tmp/chat-screen.txt"
wait_exit
logs | tee "$smoke_tmp/chat.log"
[[ "$(grep -Fc 'Verified source IDs:' "$smoke_tmp/chat.log")" -eq 2 ]]
printf 'Aspire local models, tools, persistence and two-turn terminal chat passed.\n'
