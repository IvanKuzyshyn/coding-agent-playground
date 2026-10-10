# Terrarium

A single-machine prototype of an internal platform for remote coding-agent sessions. An engineer
starts Claude Code or Codex sessions in isolated, disposable Docker sandboxes, then monitors them,
sends follow-up prompts, attaches to the live stream and stops them from any device, over an HTTP
API or a web UI. Sessions run on the engineer's Claude Max / ChatGPT Plus subscription, and the
real credentials never enter a sandbox.

- Requirements: `docs/initial-requirements/requirements.txt`
- Plan and architecture: `docs/plan/local-prototype-plan.md`
- Threat model (demonstrated vs design-only controls): `docs/threat-model.md`
- Contracts: `docs/contracts/` (API, sandboxd, gateway)
- Spikes that proved the credential swap: `spikes/s1-claude-swap`, `spikes/s2-codex-swap`

```mermaid
flowchart LR
    E[Engineer: browser / curl] --> WEB[web: Next.js] --> API
    E --> API[server: API + supervisor]
    API --> PG[(Postgres)]
    API -->|Docker API| SB[sandboxes on internal network]
    SB -->|placeholders| GW[gateway: real tokens, CONNECT allowlist]
    GW --> V[Anthropic / OpenAI / allowlisted hosts]
```

## What was built vs reused

| Built | Reused |
|---|---|
| Go control plane: API, reconcile-loop supervisor, Docker provider, harness adapters (`internal/`) | Claude Code, Codex CLI (pinned) |
| Credential-swapping gateway + CONNECT allowlist proxy (`internal/gateway`) | Docker Engine, Postgres, Docker Compose |
| `sandboxd` in-sandbox exec daemon (`cmd/sandboxd`) | Next.js, pgx, Go stdlib HTTP |
| Web UI (`web/`) | |

## Prerequisites

macOS with Docker Desktop, Go 1.27, Node 24, `jq`, `openssl`, a Claude Max and/or ChatGPT Plus
subscription, and the `claude` and `codex` CLIs on the host (for the one-time logins), optionally `gh`.

## Setup (once)

```bash
# Claude token and GitHub token -> macOS keychain; Codex login -> ~/.terrarium/codex
# (separate from ~/.codex). Pick the tools, then follow each login.
make credentials

# Engineer API token (printed once)
make token NAME=alice
```

## Run

```bash
make up        # builds images, applies migrations, starts postgres, server, gateway, web
```

- Web UI: http://127.0.0.1:3000 (log in with the engineer token)
- API: http://127.0.0.1:8080 (`Authorization: Bearer <token>`), see `docs/contracts/api.md`
- From another device: expose the UI over a private network, e.g. `tailscale serve 3000` (HTTPS).
  Never publish the ports on a public interface.

## Demo script (API)

```bash
T=<engineer token>; H="Authorization: Bearer $T"
# start a session on a public repo
ID=$(curl -s -XPOST localhost:8080/sessions -H "$H" \
  -d '{"name":"explore hello world","harness":"claude-code","repo":"octocat/Hello-World","prompt":"What is this repo?"}' | jq -r .id)
# attach to the live stream (Ctrl-C to detach; reconnect any time with Last-Event-ID)
curl -N localhost:8080/sessions/$ID/events -H "$H" -H 'Accept: text/event-stream'
# follow-up, stop the running turn, inspect egress decisions, delete
curl -s -XPOST localhost:8080/sessions/$ID/turns -H "$H" -d '{"prompt":"Now try: curl https://example.com"}'
curl -s -XPOST localhost:8080/sessions/$ID/turns/current/stop -H "$H"
curl -s "localhost:8080/sessions/$ID/events?kind=egress" -H "$H" | jq '.events[].payload'
curl -s -XDELETE localhost:8080/sessions/$ID -H "$H"

# push and open a PR as you (needs GitHub in `make credentials`); the agent may only push to
# terrarium/<session-id>/* branches of the session's repo
curl -s -XPOST localhost:8080/sessions -H "$H" \
  -d '{"name":"readme fix","harness":"codex","repo":"<you>/<repo>","prompt":"Fix a typo in README.md, push and open a PR."}'
```

## Use it from another agent (MCP)

Terrarium is also an MCP server at `http://127.0.0.1:8080/mcp` with tools to list presets, create,
wait for, inspect, follow up and terminate sessions (`docs/contracts/mcp.md`). The web UI's **MCP**
page has copy-paste setup; for Claude Code:

```bash
export TERRARIUM_TOKEN=<engineer token>
claude mcp add --transport http --scope user terrarium http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer $TERRARIUM_TOKEN"
```

## Tests

```bash
make deps-up
TERRARIUM_TEST_DATABASE_URL='postgres://terrarium:terrarium@127.0.0.1:55432/terrarium?sslmode=disable' \
  go test -race ./...
(cd web && npm ci && npm run lint && npm run typecheck && npm run build)
```

The Go suite covers the state machine and supervisor (crash and reattach, stop, TTL, idle timeout,
lost sandbox, orphan cleanup, advisory lock), the gateway (token swap, route allowlist, budget,
usage accounting, Codex refresh, CONNECT allowlist, GitHub repository/branch checks), sandboxd and the API (auth, ownership, limits,
SSE).

## Not in this prototype

IaC for a real deployment, Kubernetes, microVM isolation, per-engineer GitHub credentials (one
`gh` token for the deployment today), mid-turn steering. The threat model and plan describe how they fit.
