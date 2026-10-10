#!/usr/bin/env bash
# Phase 2 of spike S1: run Claude Code through the gateway with the real Max token.
# The token is read from the macOS keychain into the gateway container only; it is never printed
# or written to disk. Store it first with:
#   security add-generic-password -a "$USER" -s terrarium-claude-oauth -w
set -euo pipefail
cd "$(dirname "$0")"

A=terrarium-s1-agent-1
G=terrarium-s1-gateway-1
PROMPT_ENV=(-e CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1)

REAL_CLAUDE_OAUTH_TOKEN="$(security find-generic-password -a "$USER" -s terrarium-claude-oauth -w)"
export REAL_CLAUDE_OAUTH_TOKEN
docker compose up -d --build --force-recreate --wait >/dev/null 2>&1
since=$(date -u +%Y-%m-%dT%H:%M:%SZ)

echo "== 1. turn 1"
out1=$(docker exec "${PROMPT_ENV[@]}" "$A" claude -p "Remember the number 41. Reply with the word ok." \
  --output-format stream-json --verbose --max-turns 2)
echo "$out1" | jq -c 'select(.type=="result") | {subtype, is_error, result, num_turns, usage: {input_tokens: .usage.input_tokens, output_tokens: .usage.output_tokens}}'
sid=$(echo "$out1" | jq -r 'select(.type=="system" and .subtype=="init") | .session_id')

echo "== 2. turn 2 (resume $sid)"
docker exec "${PROMPT_ENV[@]}" "$A" claude -p "What number did I ask you to remember? Reply with the number only." \
  --resume "$sid" --output-format stream-json --verbose --max-turns 2 \
  | jq -c 'select(.type=="result") | {subtype, is_error, result}'

echo "== 3. real token absent from sandbox env and filesystem"
docker inspect "$A" --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -cF "$REAL_CLAUDE_OAUTH_TOKEN" || true
docker export "$A" | grep -caF "$REAL_CLAUDE_OAUTH_TOKEN" || true

echo "== 4. non-placeholder bearer is refused"
docker exec "$A" curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer sk-ant-oat01-guess" \
  -X POST http://gateway:8080/anthropic/v1/messages

echo "== 5. real token absent from gateway logs"
docker logs "$G" 2>&1 | grep -cF "$REAL_CLAUDE_OAUTH_TOKEN" || true

echo "== 6. egress decisions during this run"
docker logs --since "$since" "$G" 2>&1 | grep -E "egress (denied|allowed)" | sort | uniq -c || echo "none"
