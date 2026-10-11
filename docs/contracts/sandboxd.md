# sandboxd and agent image contract (v0)

`sandboxd` is the only process the container starts. The supervisor drives everything through it.

## Process

- Listens on `:7777`, plain HTTP on the internal `terrarium-sandbox` network.
- Reads `SANDBOXD_TOKEN` from its environment at startup, then **unsets it**. Child commands never
  inherit it. Every endpoint except `/healthz` requires `Authorization: Bearer <SANDBOXD_TOKEN>`
  (constant-time compare) → otherwise `401`. Other sandboxes share the network, so the token is
  what stops them from driving this one.
- Runs as the image's non-root user `agent` (uid 1000), `HOME=/home/agent`.
- Command state lives in memory plus log files under `/home/agent/.sandboxd/`. It does not need to
  survive a sandboxd restart (a container restart kills the commands too).

## Endpoints

| Method | Path | Behaviour |
|---|---|---|
| `GET` | `/healthz` | `200 {"ok":true}`, no auth |
| `PUT` | `/files?path=<abs>&mode=0644` | Write body to `path` (create parent dirs, atomic rename). `204` |
| `GET` | `/files?path=<abs>` | File bytes, `404` if missing |
| `POST` | `/commands` | Body `{"id":"...", "argv":["..."], "env":{"K":"V"}, "cwd":"/home/agent/work"}`. Starts the command in its own process group, stdin `/dev/null`, stdout → `.sandboxd/<id>.out`, stderr → `.sandboxd/<id>.err`. **Idempotent by `id`:** if `id` exists, return its current status and start nothing. `200 Command` |
| `GET` | `/commands/{id}` | `200 Command`, `404` if unknown |
| `GET` | `/commands/{id}/logs?stream=stdout\|stderr&offset=N` | Body: bytes from `offset`, at most 1 MiB. Headers `X-Next-Offset: <n>`, `X-Command-State: running\|exited`. `404` if unknown |
| `POST` | `/commands/{id}/signal` | Body `{"signal":"TERM"\|"KILL"}`, sent to the process group. `204`; `409` if exited |

`Command`: `{"id":"...","state":"running"|"exited","exit_code":0|null,"started_at":"RFC3339","ended_at":"RFC3339"|null}`

Child env = sandboxd's env (minus `SANDBOXD_TOKEN`) + `env` from the request. `id` matches
`^[A-Za-z0-9._-]{1,128}$`. Paths must be absolute and clean.

A reader that sees `X-Command-State: exited` and gets an empty body at `X-Next-Offset` has read
all output (the state is captured after the process's pipes are closed and logs flushed).

## Agent image (`platform/images/agent/Dockerfile`, built from `platform/`; Claude Code and Codex versions from `mise.toml`)

- Base `node:24-bookworm-slim`; `git curl ca-certificates jq ripgrep python3 procps`.
- Pinned `@anthropic-ai/claude-code@2.1.296` and `@openai/codex@0.162.1`.
- `sandboxd` built from `platform/cmd/sandboxd` in a Go build stage (static).
- User `agent` uid/gid 1000, `HOME=/home/agent`, workdir `/home/agent/work`, `ENTRYPOINT ["/usr/local/bin/sandboxd"]`.
- `/etc/claude-code/managed-settings.json`: `{"permissions":{"deny":["WebFetch","WebSearch"]}}`
  (defence in depth; the sandbox has no direct internet anyway).

## How the supervisor runs it

- `docker create` with: `--read-only`, tmpfs `/tmp`, a per-session volume at `/home/agent`,
  `--cap-drop ALL`, `--security-opt no-new-privileges`, `--pids-limit`, CPU/memory limits, network
  `terrarium-sandbox` only, label `terrarium.session-id=<id>`, env `SANDBOXD_TOKEN` + harness env.
- Because `/home/agent` is a fresh volume, sandboxd must create `/home/agent/.sandboxd` and
  `/home/agent/work` at startup.
