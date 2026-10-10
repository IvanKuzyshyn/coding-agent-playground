# Gateway contract (v0)

`terrarium gateway` holds the only real credentials. Sandboxes reach it at `gateway:8080`
(inference) and `gateway:3128` (CONNECT proxy) on the internal `terrarium-sandbox` network.
Validated by `spikes/s1-claude-swap` and `spikes/s2-codex-swap`; reuse their findings.

## Session resolution

Every request carries the per-session secret in one of these forms (`internal/credentials`):

| Where | Form | Extract with |
|---|---|---|
| Claude inference | `Authorization: Bearer sk-ant-oat01-terrarium-<secret>` | `credentials.SecretFromBearer` |
| Codex inference | `Authorization: Bearer <unsigned JWT with terrarium_secret claim>` | `credentials.SecretFromBearer` |
| Codex refresh | form/JSON field `refresh_token=terrarium-refresh-<secret>` | `credentials.SecretFromCodexRefreshToken` |
| CONNECT | `Proxy-Authorization: Basic base64("terrarium:<secret>")` | – |
| GitHub (`/github/`) | `Authorization: Basic base64("terrarium:<secret>")` | – |

Then `store.GatewaySessionBySecretHash(credentials.Hash(secret))`. Unknown → `403`.
Inference requires `session.Live(status)`; CONNECT and `/github/` require
`session.EgressAllowed(status)` (the repository is cloned while provisioning).
The route's harness must match the session's harness. Never log secrets, bearer values or real
tokens; log header names only.

## Listener `:8080`

| Route | Harness | Upstream | Notes |
|---|---|---|---|
| `GET /healthz` | – | – | `200 {"ok":true}` |
| `POST /anthropic/v1/messages` | claude-code | `https://api.anthropic.com/v1/messages` | Keep query string. Replace `Authorization` with the real Max token. Forward `anthropic-beta` unchanged. Drop `X-Api-Key` |
| `POST /anthropic/v1/messages/count_tokens` | claude-code | same host | same |
| `POST /openai/responses` | codex | `https://chatgpt.com/backend-api/codex/responses` | Replace `Authorization` and `ChatGPT-Account-ID` with the real values |
| `GET /openai/models` | codex | `https://chatgpt.com/backend-api/codex/models` | same |
| `POST /oauth/token` | codex | none | Answer with placeholder tokens for the same session: `{"id_token","access_token","refresh_token"}` taken from `credentials.CodexAuthJSON(secret, now)` |
| anything else | – | – | `403`, egress event `denied` |

Match routes exactly on method + path; reject any request whose `URL.RawPath` is non-empty.
Stream responses (`FlushInterval: -1`).

**Real credentials:**
- Claude: token string in `TERRARIUM_CLAUDE_TOKEN_FILE` (trim whitespace).
- Codex: `auth.json` in `TERRARIUM_CODEX_AUTH_FILE`, a login dedicated to the gateway. The gateway
  is its **only** refresher: when the access token's `exp` is < 1 h away, or upstream returns
  `401`, refresh single-flight against `https://auth.openai.com/oauth/token` exactly as Codex does
  (check the Codex source for client id and body), write the rotated tokens back atomically
  (temp file + rename, mode 0600), retry the request once.
- Missing credential for a harness → `503` for that harness's routes; the other harness still works.

**Token budget:** before forwarding inference, if `TokensUsed >= TokenBudget` → `429`
`{"error":"session token budget exhausted"}` and egress event `denied` reason `budget`. While
streaming the response through, parse usage and call `store.AddTokensUsed` once per response:
- Claude SSE: `message_start.message.usage` (`input_tokens` + `cache_creation_input_tokens`) and
  the final `message_delta.usage.output_tokens`; non-streaming JSON: top-level `usage`.
- Codex SSE: `response.completed` → `response.usage` (`input_tokens` − `input_tokens_details.cached_tokens` + `output_tokens`).
Count = uncached input + cache writes + output. Usage parsing must never block or alter the stream.

## GitHub route `/github/` (listener `:8080`)

Sandboxes get `GIT_CONFIG_*` env rewriting `https://github.com/` to
`http://terrarium:<secret>@gateway:8080/github/`, so every git remote goes through the gateway.
Only the session's `spec.repo` is reachable (case-insensitive, `.git` optional); upstream paths use
the spec's spelling.

| Route | Upstream | Checks |
|---|---|---|
| `GET /github/{repo}.git/info/refs?service=git-upload-pack` | `https://github.com/{repo}.git/...` | only the `service` query parameter |
| `POST /github/{repo}.git/git-upload-pack` | same | – |
| `GET /github/{repo}.git/info/refs?service=git-receive-pack` | same | GitHub token required |
| `POST /github/{repo}.git/git-receive-pack` | same | Every ref update is a branch (`refs/heads/*`) and not a delete. Existing branches (non-zero old id) may be updated; new branches (zero old id) must be `refs/heads/terrarium/<session-id>/*`. GitHub applies an update only if the old id matches, so a non-zero old id cannot create a branch. Encoded bodies `415` |
| `POST /github/api/repos/{repo}/pulls` | `https://api.github.com/repos/{repo}/pulls` | JSON `head` is a branch name of this repository (no `owner:` fork prefix) |
| anything else | – | `403`, egress event `denied` |

- No `Authorization` → `401` with `WWW-Authenticate: Basic` (git only sends credentials after a
  challenge).
- Real credential: token string in `TERRARIUM_GITHUB_TOKEN_FILE`. git routes get
  `Basic base64("x-access-token:<token>")`, the API route `Bearer <token>`.
- Without a token, fetches are forwarded unauthenticated (public repositories still clone);
  pushes and PRs → `503`.
- Pushes and PR creations are recorded as egress events `allowed` (`via: github`, with `refs` or
  `head`); fetches are not.

## Listener `:3128` (CONNECT proxy)

- Only `CONNECT`; anything else `403`. Require `Proxy-Authorization` (`407` with
  `Proxy-Authenticate: Basic` if missing).
- Allow only port `443` and hosts exactly in the session's `Allow` list.
- Forward bytes already buffered after hijack.
- Egress event per decision: `{"decision","via":"connect","host"}`.

## Events

Write egress decisions with `store.AppendEvent(ctx, sessionID, store.EventEgress, nil, payload)`.
Allowed inference requests are not logged as events (they are visible as harness events); denied
ones are.
