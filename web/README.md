# Terrarium web UI

Next.js (App Router, TypeScript) console for Terrarium sessions. It is a backend-for-frontend over
the HTTP API in `docs/contracts/api.md`: the browser talks only to this app; server components,
server actions and the SSE proxy route call the API with the engineer token, which lives only in an
encrypted (`jose`, A256GCM), httpOnly, SameSite=Lax cookie.

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `TERRARIUM_API_URL` | `http://127.0.0.1:8080` | Base URL of the Terrarium API (server-side only) |
| `TERRARIUM_WEB_SECRET` | dev-only fallback | Cookie encryption secret. Required in production: the server exits at startup without it. Use a long random value (`openssl rand -base64 32`) |
| `TERRARIUM_INSECURE_COOKIE` | unset | Set to `1` to drop the `Secure` cookie flag in production when serving over plain http from a host other than `localhost` |
| `PORT` | `3000` | Listen port |

## Development against the mock API

The mock (`mock/server.mjs`, no dependencies) implements the API contract in memory, including SSE,
and simulates the session lifecycle with Claude- and Codex-shaped events and an egress denial.
Login token: `dev-token` (`other-token` is a second engineer, for 404 checks). Cap: 3 active sessions.

```sh
npm install
npm run mock                                   # API mock on :8081
TERRARIUM_API_URL=http://127.0.0.1:8081 npm run dev   # UI on :3000
```

`MOCK_PORT` and `MOCK_STEP_MS` (delay between simulated events) tune the mock.

## Checks

```sh
npm run lint
npm run typecheck    # next typegen && tsc --noEmit (route types are generated)
npm run build
```

## Production

```sh
npm run build
TERRARIUM_API_URL=http://127.0.0.1:8080 TERRARIUM_WEB_SECRET=... node .next/standalone/server.js
```

(`next start` also works but warns because the build uses `output: "standalone"`; copy
`.next/static` next to the standalone server if you run it outside the Docker image.)

Docker (multi-stage, standalone output, runs as the non-root `node` user):

```sh
docker build -t terrarium-web:local web
docker run --rm -p 3000:3000 \
  -e TERRARIUM_API_URL=http://host.docker.internal:8080 \
  -e TERRARIUM_WEB_SECRET=... terrarium-web:local
```

Bind to localhost and reach it from other devices over a private overlay (e.g. Tailscale); never
publish it on a public interface.
