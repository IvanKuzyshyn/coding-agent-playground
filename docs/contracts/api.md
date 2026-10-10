# HTTP API contract (v1)

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
| `POST` | `/sessions` | `{"name":"...", "harness":"claude-code"\|"codex", "prompt":"...", "repo":"owner/name"?, "preset":"default"?, "model":"..."?}` | `202 Session` |
| `GET` | `/sessions` | query: see [Listing](#listing) | `200 {"sessions":[Session], "total": <matching count>}` without `turns` |
| `GET` | `/stats` | – | `200 Stats` for the caller's sessions |
| `GET` | `/sessions/{id}` | – | `200 Session` with `turns` |
| `POST` | `/sessions/{id}/turns` | `{"prompt":"..."}` | `202 Turn` (status `queued`); `409` if the session is not `pending`/`provisioning`/`idle`/`running` |
| `POST` | `/sessions/{id}/turns/current/stop` | – | `202 {}`; `409` if no turn is running |
| `DELETE` | `/sessions/{id}` | – | `202 Session` (status becomes `stopping`, then `stopped`) |
| `GET` | `/sessions/{id}/events` | query `after`, `kind`, `limit` (default 500, max 1000) | `200 {"events":[Event],"next_after":<id>}` |
| `GET` | `/sessions/{id}/events` with `Accept: text/event-stream` | query `after` or header `Last-Event-ID` | SSE stream (below) |

Validation: `name` required, 1–120 characters after trimming, not unique (sessions are identified
by `id` only). `prompt` non-empty, ≤ 100 KB. `repo` is `owner/name` (public GitHub repo, cloned into
`/home/agent/work`). Creating a session beyond the per-engineer active-session cap → `429`.

## Listing

`GET /sessions` query parameters (all optional, combinable):

| Param | Values | Default |
|---|---|---|
| `q` | case-insensitive substring of `name` | – |
| `status` | comma-separated `display_status` values: `pending`, `provisioning`, `idle`, `running`, `stopping`, `stopped`, `failed` | all |
| `harness` | comma-separated `claude-code`, `codex` | all |
| `sort` | `created_at`, `duration`, `cost`, `tokens`, `name`, `status` (lifecycle order: pending, provisioning, idle, running, stopping, stopped, failed) | `created_at` |
| `order` | `asc`, `desc` | `desc` |
| `limit` | 1–200 | 50 |
| `offset` | ≥ 0 | 0 |

Unknown values → `400`. `total` is the number of sessions matching the filters (for pagination).

## Objects

```jsonc
// Session
{
  "id": "uuid",
  "owner": "alice",
  "name": "fix flaky test",
  "status": "pending|provisioning|idle|running|stopping|stopped|failed",
  // status, except a session that ended because of an error shows "failed" even after cleanup
  // moved it to "stopped". Filters use this.
  "display_status": "pending|provisioning|idle|running|stopping|stopped|failed",
  "harness": "claude-code|codex",
  "preset": "default",
  "repo": "owner/name",            // omitted if none
  "model": "...",                  // omitted if harness default
  "tokens_used": 12345,
  "token_budget": 2000000,
  // API-equivalent cost estimate in USD, computed by the gateway from list prices
  // (config/pricing.yaml). Subscriptions are not billed per token; this is for comparison only.
  "cost_usd": 0.42,
  // false if some requests used a model missing from the price table (cost_usd is then a lower bound)
  "cost_complete": true,
  // seconds from created_at to ended_at, or to now while the session is not stopped
  "duration_seconds": 312,
  "error": "...",                  // omitted if none
  "created_at": "RFC3339", "updated_at": "RFC3339", "expires_at": "RFC3339",
  "idle_until": "RFC3339",         // omitted if none
  "ended_at": "RFC3339",           // omitted if none
  "turn_count": 2,
  "current_turn": 2,               // seq of the queued/running turn, omitted if none
  "turns": [Turn]                  // only on GET /sessions/{id}
}

// Stats (caller's sessions)
{
  "total": 39,          // all sessions ever created
  "running": 2,         // display_status running (a turn is executing)
  "active": 5,          // not stopped (pending, provisioning, idle, running, stopping)
  "failed": 3,          // display_status failed
  "failure_rate": 0.0769 // failed / ended sessions (stopped + failed); 0 when none ended
}

// Turn
{
  "seq": 1,
  "prompt": "...",
  "status": "queued|running|complete|failed|stopped|limit_exceeded",
  "result": "final assistant message",    // omitted until finished
  "error": "...",                         // omitted if none
  // Per turn. input_tokens excludes cached tokens for both harnesses. agent_steps is reported by
  // Claude only (0 for Codex). Codex turns also carry thread_input_tokens, thread_output_tokens and
  // thread_cached_input_tokens: the thread totals the per-turn values are derived from.
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
