# Sandbox image: Claude Code, Codex and sandboxd. Build from the repo root:
#   docker build -t terrarium-agent:local -f images/agent/Dockerfile .

FROM golang:1.27-bookworm AS sandboxd
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/sandboxclient internal/sandboxclient
COPY cmd/sandboxd cmd/sandboxd
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/sandboxd ./cmd/sandboxd

FROM node:24-bookworm-slim

ARG CLAUDE_CODE_VERSION=2.1.296
ARG CODEX_VERSION=0.162.1

RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates jq ripgrep python3 procps \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} @openai/codex@${CODEX_VERSION} \
 && npm cache clean --force

# The base image's "node" user holds uid/gid 1000, which the contract assigns to "agent".
RUN userdel --remove node \
 && groupadd --gid 1000 agent \
 && useradd --uid 1000 --gid 1000 --create-home --home-dir /home/agent --shell /bin/bash agent \
 && install -d -o agent -g agent /home/agent/work

COPY images/agent/managed-settings.json /etc/claude-code/managed-settings.json
COPY --from=sandboxd /out/sandboxd /usr/local/bin/sandboxd
COPY --chmod=755 images/agent/terrarium-pr /usr/local/bin/terrarium-pr

USER agent
ENV HOME=/home/agent
WORKDIR /home/agent/work
EXPOSE 7777
ENTRYPOINT ["/usr/local/bin/sandboxd"]
