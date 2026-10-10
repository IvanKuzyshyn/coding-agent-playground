# Resilience and security drills

Run live against the full Compose stack on 2026-10-10 (Docker Desktop 29.5.3, Claude Code 2.1.296,
Codex 0.162.1). The automated equivalents are in `platform/internal/ops`, `platform/internal/supervisor`,
`platform/internal/gateway` and `platform/internal/api` tests.

| # | Drill | How | Result |
|---|---|---|---|
| 1 | Server killed mid-turn | `docker kill terrarium-server-1` 5 s into a 25 s shell loop, restart after 12 s | Supervisor re-attached to the same sandboxd command (`turn-1` was the only command ever started), one `claude` process throughout, no duplicated harness events, turn `complete` with the expected result |
| 2 | Sandbox killed mid-turn | `docker kill terrarium-sbx-<id>` during `sleep 60` | Within ~1 s: turn `failed: sandbox lost`, session `stopped`, container and home volume removed |
| 3 | Orphan sandbox | Start a container with Terrarium labels and an unknown session id | Removed by the next reconcile pass (≤ 5 s) |
| 4 | Delete during a running turn | `DELETE /sessions/{id}` while Codex runs | Turn stopped, session `stopped`, container and volume removed in seconds |
| 5 | Stop a running turn | `POST /sessions/{id}/turns/current/stop` during `sleep 120` (web UI) | Turn `stopped` (exit 137 after TERM), session back to `idle`, follow-ups still work |
| 6 | Exfiltration attempt | Agent asked to `curl https://example.com`, then with `--noproxy '*'` | CONNECT `403` recorded as egress `denied`; without proxy DNS fails |
| 7 | Credential search | Agent asked to `env \| grep -i token`; `docker export` / `docker inspect` of the sandbox; grep of all service logs | Real Claude and Codex tokens: 0 matches anywhere outside the gateway |
| 8 | Subscription quota exhausted | Real ChatGPT Plus account at its usage limit | Turn `failed` with the vendor's message ("You've hit your usage limit … try again at …"), session stays `idle` and usable |
| 9 | Sandbox calls the control-plane API | `curl http://server:8080/healthz` from a sandbox | `403` (source subnet refused) |
| 10 | Codex end to end | Two Codex turns on a cloned repo, then follow-ups | Turn 2 resumed the thread (`41`); gateway token count per turn equals the turn's reported usage (408 = 403 + 5) |
| 11 | Unknown user / other user's session | Requests without token; changes with another user's token | `401` / `403`, reads allowed (API tests) |
