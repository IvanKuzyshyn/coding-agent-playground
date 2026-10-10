# MCP contract (v1)

The server exposes the session operations of the [HTTP API](api.md) as MCP tools, so another agent
(Claude Code, Codex, Cursor…) can start, follow and end Terrarium sessions.

- Endpoint: `POST http://127.0.0.1:8080/mcp`, Streamable HTTP, stateless (no `Mcp-Session-Id`),
  JSON responses. Same process and port as the REST API (`platform/internal/api/mcp.go`, go-sdk v1.8).
- Auth: `Authorization: Bearer <user token>` (from `POST /login` or the web UI's user menu),
  verified like the REST API; missing or unknown → `401`. Sessions created over MCP belong to that
  user and appear in the web UI.
- Requests from the sandbox network are refused (`403`), as for the REST API.
- Tools share the REST handlers' operations (validation, preset resolution, active-session cap,
  visibility: every user can read every session; only its owner can follow up or terminate it). Caller mistakes come back as tool
  errors (`isError: true`) with the message; internal failures as "internal error".

## Tools

| Tool | Input | Output |
|---|---|---|
| `list_options` | – | `harnesses`; `presets[]` with `allowed_hosts`, `max_agent_steps`, `token_budget`, `idle_timeout_seconds`, `ttl_seconds`, `lifecycle` |
| `create_session` | `name`, `harness`, `prompt`, `repo`?, `preset`?, `model`?, `keep_idle`? (as `POST /sessions`) | Session |
| `get_session` | `id`, `include_events`? (last 25 lifecycle and 25 egress events), `max_result_chars`? (default 4000) | Session with `turns` |
| `wait_for_session` | `id`, `timeout_seconds`? (default 120, max 600), `max_result_chars`? | Session with `turns`; `timed_out: true` if it had not settled. Settled = terminal, or `idle` with no queued turn |
| `list_sessions` | `owner`? (default: every user), `status[]`?, `harness[]`?, `query`?, `limit`? (default 20, max 200) | `{sessions[], total}`, newest first |
| `send_follow_up` | `id`, `prompt` | `{seq, status}`; error if the session is not accepting turns |
| `terminate_session` | `id` | Session (`stopping`, then terminal; see [Lifecycle](api.md#lifecycle)) |

Session (MCP): `id`, `owner`, `name`, `status`, `harness`, `preset`, `repo`, `model`, `lifecycle`, `error`,
`tokens_used`, `token_budget`, `cost_usd`, `duration_seconds`, `created_at`, `ended_at`,
`turn_count`, `current_turn`, `ui_url`, and where noted `turns[]` (`seq`, `prompt`, `status`,
`result`, `truncated`, `error`, `usage`, `started_at`, `ended_at`) and `events[]`.

## Client setup

```bash
export TERRARIUM_TOKEN=<your token>
claude mcp add --transport http --scope user terrarium http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer $TERRARIUM_TOKEN"
codex mcp add terrarium --url http://127.0.0.1:8080/mcp --bearer-token-env-var TERRARIUM_TOKEN
```

The web UI's MCP page shows the same commands with the token filled in: masked on screen, copied in
full.
