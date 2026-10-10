#!/usr/bin/env bash
# Phase 2 of spike S2: run Codex through the gateway with the real ChatGPT login.
# The host's ~/.codex/auth.json is mounted read-only into the gateway container only. The spike
# gateway does not refresh tokens, so refresh the host login right before running:
#   codex login status   (or run any codex command on the host)
set -euo pipefail
cd "$(dirname "$0")"

A=terrarium-s2-agent-1
G=terrarium-s2-gateway-1
REAL_AUTH="$HOME/.codex/auth.json"
real_access=$(jq -r '.tokens.access_token' "$REAL_AUTH")

./forge.sh >/dev/null
PLACEHOLDER_ACCESS_TOKEN="$(cat .generated/access_token)" REAL_CODEX_AUTH_JSON="$REAL_AUTH" \
  docker compose up -d --build --force-recreate --wait >/dev/null 2>&1
docker exec "$A" mkdir -p /home/agent/.codex /home/agent/work
docker exec "$A" cp /etc/terrarium/codex/auth.json /etc/terrarium/codex/config.toml /home/agent/.codex/
since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
codex_exec() { docker exec -w /home/agent/work "$A" codex exec --json --dangerously-bypass-approvals-and-sandbox "$@" </dev/null 2>/dev/null; }

echo "== 1. turn 1"
out1=$(codex_exec "Remember the number 41. Reply with the word ok.")
echo "$out1" | jq -c 'select(.type=="item.completed" or .type=="turn.completed" or .type=="turn.failed" or .type=="error")'
tid=$(echo "$out1" | jq -r 'select(.type=="thread.started") | .thread_id')

echo "== 2. turn 2 (resume $tid)"
codex_exec resume "$tid" "What number did I ask you to remember? Reply with the number only." \
  | jq -c 'select(.type=="item.completed" or .type=="turn.completed" or .type=="turn.failed" or .type=="error")'

echo "== 3. real access token absent from sandbox env and filesystem (expect 0 and 0)"
docker inspect "$A" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -cF "$real_access" || true
docker export "$A" | grep -caF "$real_access" || true

echo "== 4. non-placeholder bearer is refused (expect 403)"
docker exec "$A" curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer guess" \
  -X POST http://gateway:8080/openai/responses

echo "== 5. real access token absent from gateway logs (expect 0)"
docker logs "$G" 2>&1 | grep -cF "$real_access" || true

echo "== 6. routes and egress during this run"
docker logs --since "$since" "$G" 2>&1 | grep -oE '(denied|inference request|refresh request) method=[A-Z]+ path=[^ ]+' | sort | uniq -c
