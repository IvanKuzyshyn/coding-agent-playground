#!/usr/bin/env bash
# Writes placeholder Codex credentials and config for the sandbox into ./.generated/.
# Tokens are unsigned JWTs with only the claims Codex 0.162.1 parses; Codex never verifies them.
set -euo pipefail
cd "$(dirname "$0")"
out=.generated
mkdir -p "$out"

b64url() { printf '%s' "$1" | base64 | tr '+/' '-_' | tr -d '=\n'; }
jwt() { printf '%s.%s.%s' "$(b64url '{"alg":"none","typ":"JWT"}')" "$(b64url "$1")" "placeholder"; }

account="acct-terrarium-placeholder"
id_token=$(jwt '{"email":"agent@terrarium.local","https://api.openai.com/auth":{"chatgpt_plan_type":"plus","chatgpt_account_id":"'"$account"'"}}')
# Far-future exp, so Codex never tries to refresh on its own.
access_token=$(jwt '{"exp":4102444800,"terrarium_session":"s2"}')

cat > "$out/auth.json" <<EOF
{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"last_refresh":"$(date -u +%Y-%m-%dT%H:%M:%SZ)",
 "tokens":{"id_token":"$id_token","access_token":"$access_token","refresh_token":"placeholder-refresh","account_id":"$account"}}
EOF

# Custom provider instead of the built-in "openai": plain HTTP SSE (no WebSocket) and no
# /wham/accounts/check preflight. chatgpt_base_url points at the gateway so stray calls are visible.
cat > "$out/config.toml" <<'EOF'
model_provider = "gw"
chatgpt_base_url = "http://gateway:8080/chatgpt/"
cli_auth_credentials_store = "file"

[model_providers.gw]
name = "gw"
base_url = "http://gateway:8080/openai"
wire_api = "responses"
requires_openai_auth = true

[analytics]
enabled = false

[otel]
metrics_exporter = "none"
EOF

printf '%s' "$access_token" > "$out/access_token"
echo "wrote $out/auth.json $out/config.toml"
