# HTTP API contract (v0)

Base URL: `http://127.0.0.1:8080`. JSON in and out. Errors: `{"error": "<message>"}` with a 4xx/5xx
status.

**Auth:** every endpoint except `/healthz` requires `Authorization: Bearer <engineer token>`
(`make token NAME=...`). Missing or unknown token → `401`. A session owned by someone else behaves
as if it does not exist (`404`).

## Endpoints

| Method | Path | Body | Success |
|---|---|---|---|
| `GET` | `/healthz` | – | `200 {"ok":true}` |
| `GET` | `/whoami` | – | `200 {"name":"alice"}` |
| `GET` | `/presets` | – | `200 {"presets":[{"name":"default"},{"name":"explore"}]}` |
| `POST` | `/sessions` | `{"harness":"claude-code"\|"codex", "prompt":"...", "repo":"owner/name"?, "preset":"default"?, "model":"..."?}` | `202 Session` |
| `GET` | `/sessions` | – | `200 {"sessions":[Session]}` newest first, without `turns` |
| `GET` | `/sessions/{id}` | – | `200 Session` with `turns` |
| `POST` | `/sessions/{id}/turns` | `{"prompt":"..."}` | `202 Turn` (status `queued`); `409` if the session is not `pending`/`provisioning`/`idle`/`running` |
| `POST` | `/sessions/{id}/turns/current/stop` | – | `202 {}`; `409` if no turn is running |
| `DELETE` | `/sessions/{id}` | – | `202 Session` (status becomes `stopping`, then `stopped`) |
| `GET` | `/sessions/{id}/events` | query `after`, `kind`, `limit` (default 500, max 1000) | `200 {"events":[Event],"next_after":<id>}` |
| `GET` | `/sessions/{id}/events` with `Accept: text/event-stream` | query `after` or header `Last-Event-ID` | SSE stream (below) |

Validation: `prompt` non-empty, ≤ 100 KB. `repo` is `owner/name` (public GitHub repo, cloned into
`/home/agent/work`). Creating a session beyond the per-engineer active-session cap → `429`.

## Objects

```jsonc
// Session
{
  "id": "uuid",
  "owner": "alice",
  "status": "pending|provisioning|idle|running|stopping|stopped|failed",
  "harness": "claude-code|codex",
  "preset": "default",
  "repo": "owner/name",            // omitted if none
  "model": "...",                  // omitted if harness default
  "tokens_used": 12345,
  "token_budget": 2000000,
  "error": "...",                  // omitted if none
  "created_at": "RFC3339", "updated_at": "RFC3339", "expires_at": "RFC3339",
  "idle_until": "RFC3339",         // omitted if none
  "ended_at": "RFC3339",           // omitted if none
  "turn_count": 2,
  "current_turn": 2,               // seq of the queued/running turn, omitted if none
  "turns": [Turn]                  // only on GET /sessions/{id}
}

// Turn
{
  "seq": 1,
  "prompt": "...",
  "status": "queued|running|complete|failed|stopped|limit_exceeded",
  "result": "final assistant message",    // omitted until finished
  "error": "...",                         // omitted if none
  "usage": {"input_tokens": 0, "output_tokens": 0, "cached_input_tokens": 0, "agent_steps": 0},
  "created_at": "RFC3339", "started_at": "RFC3339", "ended_at": "RFC3339"
}

// Event
{"id": 42, "session_id": "uuid", "kind": "lifecycle|harness|stderr|egress|system",
 "turn_seq": 1, "payload": {...}, "ts": "RFC3339"}
```

### Event payloads

| kind | payload |
|---|---|
| `lifecycle` | `{"status":"idle","message":"sandbox ready"}`; turn transitions use `{"turn_status":"running"}` |
| `harness` | `{"harness":"claude-code"\|"codex","event":<one raw JSON line from the harness>}`. Claude: stream-json (`system/init`, `assistant`, `user` tool results, `result`). Codex: `--json` (`thread.started`, `turn.started`, `item.started`/`item.completed` with `item.type` = `agent_message`/`command_execution`/`file_change`/..., `turn.completed`, `turn.failed`, `error`) |
| `stderr` | `{"line":"..."}` |
| `egress` | `{"decision":"allowed"\|"denied","via":"connect"\|"inference","host":"...","method":"...","path":"...","reason":"..."}` |
| `system` | `{"message":"..."}` |

### SSE

```
id: 42
event: harness
data: {Event JSON}

```

The server sends events with `id > after` in order, then follows new ones (poll interval ≤ 1 s).
A comment line `: ping` is sent every 15 s. The stream ends when the session is `stopped` and all
events are flushed. Clients reconnect with `Last-Event-ID`.
