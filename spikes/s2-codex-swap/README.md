# Spike S2: Codex with placeholder ChatGPT-subscription tokens

Question: can Codex run on a ChatGPT Plus login while the real tokens stay outside the sandbox,
with a gateway swapping placeholders for them? See `docs/plan/local-prototype-plan.md` §5.3.

## Setup

- Codex CLI 0.162.1, non-root, `cap_drop: ALL`, only on `sandbox-net` (internal, no route out).
- `forge.sh` writes the sandbox's `~/.codex/auth.json` with **unsigned** JWTs carrying only the
  claims Codex parses (`chatgpt_plan_type`, `chatgpt_account_id`, far-future `exp`), and
  `config.toml`:
  - custom provider `gw` (`requires_openai_auth = true`, `wire_api = "responses"`) instead of the
    built-in `openai` provider. The built-in one always tries WebSocket and, with a base URL ending
    in `/backend-api/codex`, adds a `/wham/accounts/check` preflight. The custom provider gives plain
    HTTP SSE to `<base>/responses`.
  - `chatgpt_base_url` → gateway, so auxiliary calls are visible and denied there.
  - `[analytics] enabled = false`, `[otel] metrics_exporter = "none"`.
- Sandbox env: `CODEX_REFRESH_TOKEN_URL_OVERRIDE` / `CODEX_REVOKE_TOKEN_URL_OVERRIDE` → gateway.
- Gateway: `/openai/responses` (POST) and `/openai/models` (GET) → `https://chatgpt.com/backend-api/codex/*`
  with the real bearer and `ChatGPT-Account-ID`; `/oauth/token` answers with the placeholders;
  everything else denied. CONNECT proxy denies all hosts.

## Phase 1: fake real token (2026-10-10)

| Check | Result |
|---|---|
| Codex loads forged unsigned JWTs | Yes |
| Inference transport | `GET /openai/models`, `POST /openai/responses`, plain HTTP SSE, `Authorization: Bearer <placeholder>` + `ChatGPT-Account-ID` |
| Upstream 401 | Codex calls `POST /oauth/token` on the gateway (refresh override works), then the turn fails with `application network policy is unavailable`: a side effect of the auth reload after the failed refresh, not of the denied routes (confirmed in phase 2) |

## Phase 2: real login (2026-10-10)

`./run-real.sh` mounts the host's `~/.codex/auth.json` read-only into the gateway only.

| Check | Result |
|---|---|
| Turn 1 | `ok`; `turn.completed.usage` = 11250 input (9600 cached), 5 output |
| Turn 2 via `codex exec resume <thread_id>` | `41` (context kept) |
| Real access token in sandbox env / filesystem (`docker export`) | 0 / 0 |
| Real access token in gateway logs | 0 |
| Non-placeholder bearer | `403` |
| Requests during the run | 3× `POST /openai/responses`, 3× `GET /openai/models` forwarded. Denied without breaking the run: `/chatgpt/api/codex/accounts/check`, `/chatgpt/api/codex/settings/user`, `/chatgpt/api/codex/ps/mcp`, `/chatgpt/ps/plugins/*`, `/chatgpt/plugins/featured` |

**Conclusion:** the placeholder swap works for Codex. The real access and refresh tokens never
enter the sandbox.

## Gaps for the real gateway

- **Refresh:** the spike gateway reads `auth.json` once and never refreshes. Access tokens last
  about 10 days (observed `exp`). The real gateway must refresh against `auth.openai.com`
  single-flight, write the rotated tokens back, and be the only refresher for the account (the
  host `codex` CLI must not refresh the same login concurrently, or refresh-token rotation breaks).
- **Placeholder identity:** the per-session placeholder access token should carry a session id the
  gateway maps to a session, as in the main plan.
- **Upgrades:** Codex adds endpoints and transport changes often (WebSocket default, plugin
  endpoints). Pin the version and re-run this spike on upgrade.

## Cleanup

`PLACEHOLDER_ACCESS_TOKEN=x REAL_CODEX_AUTH_JSON=/dev/null docker compose down --rmi local`
