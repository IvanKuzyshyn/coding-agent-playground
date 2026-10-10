# Spike S1: Claude Code with a placeholder subscription token

Question: can Claude Code run on a Max subscription while the real OAuth token stays outside the
sandbox, with a gateway swapping a placeholder for it? See `docs/plan/local-prototype-plan.md` §5.2.

## Setup

- `agent`: Claude Code 2.1.296, non-root, `cap_drop: ALL`, only on `sandbox-net` (internal, no
  route out). Env holds only `CLAUDE_CODE_OAUTH_TOKEN=<placeholder>`,
  `ANTHROPIC_BASE_URL=http://gateway:8080/anthropic` and `HTTPS_PROXY=http://gateway:3128`.
- `gateway`: `:8080` reverse proxy `/anthropic/*` → `https://api.anthropic.com/*`, swaps the
  placeholder bearer for the real token, refuses any other credential. `:3128` CONNECT proxy that
  logs and denies every host not in `CONNECT_ALLOW` (empty).

## Phase 1: fake real token (2026-10-10)

`REAL_CLAUDE_OAUTH_TOKEN=fake-not-a-real-token docker compose up -d --build`

| Check | Result |
|---|---|
| Sandbox direct internet | DNS fails (`Could not resolve host`) |
| Sandbox via proxy to example.com | `403` from the CONNECT proxy |
| Claude starts with a placeholder token | Yes. No local validation of the token |
| Inference path | `POST /v1/messages` via the gateway, `Authorization: Bearer <placeholder>`, `anthropic-beta` includes `oauth-2025-04-20` |
| Upstream with fake token | `401 authentication_failed`; Claude emits `system/api_retry` then a synthetic error result. Exit code 0 |
| Direct calls bypassing `ANTHROPIC_BASE_URL` (defaults) | `api.anthropic.com/api/event_logging/v2/batch` (telemetry), bootstrap fetch, MCP registry fetch. All denied at the CONNECT proxy; the run is unaffected |
| Same with `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` | Zero direct calls. Only `/v1/messages` through the gateway |
| Token scope | Debug log: env-var / setup-token sessions have `user:inference` only; `/api/oauth/validate`-gated features (Claude in Chrome) are disabled |

### Gateway hardening after review

The first version attached the real token to any `api.anthropic.com` path. If the token's scope
allows more than inference, a sandbox could call account endpoints (e.g. mint an API key). Fixes:

| Check (placeholder bearer from the sandbox) | Result |
|---|---|
| `POST /v1/messages?beta=true` | forwarded (upstream `401` with the fake token) |
| `GET /api/oauth/profile`, `POST /api/oauth/claude_cli/create_api_key` | `403` at the gateway |
| `/v1/messages/../../api/...` (raw and percent-encoded) | `403` |
| `GET /v1/messages` (wrong method) | `403` |
| `CONNECT api.anthropic.com:8443` with the host allowlisted | refused (port must be 443) |
| DNS for external names inside the sandbox | no resolution (no DNS exfil path on this Docker version) |

Forwarded routes are an exact allowlist: `POST /v1/messages`, `POST /v1/messages/count_tokens`.

## Phase 2: real token

Store the token from `claude setup-token` in the keychain, then run `./run-real.sh`. It checks:
two-turn resume through the gateway, real token absent from sandbox env, filesystem and gateway
logs, a non-placeholder bearer refused, and egress decisions during the run.

Results (2026-10-10, Max subscription, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`):

| Check | Result |
|---|---|
| Turn 1 | `success`, result `ok` |
| Turn 2 via `--resume <session_id>` | `success`, result `41` (context kept) |
| Real token in sandbox env / filesystem (`docker export`) | 0 / 0 |
| Real token in gateway logs | 0 |
| Non-placeholder bearer | `403` |
| Requests during the run | only `POST /v1/messages` (upstream `200`); no CONNECT attempts, no denied routes |

**Conclusion:** the placeholder swap works for Claude Code on a Max subscription. The real token
never enters the sandbox. The open issue is policy, not mechanics: Anthropic's terms on
intermediating subscription tokens (plan §5.4).

## Cleanup

`REAL_CLAUDE_OAUTH_TOKEN=x docker compose down --rmi local`
