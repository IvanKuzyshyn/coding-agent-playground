# Sandbox image: Claude Code, Codex and sandboxd. Build context: platform/. The harness versions
# come from mise.toml (the same pins as the host CLIs): `mise run image-agent`.

FROM golang:1.27-bookworm AS sandboxd
WORKDIR /src
COPY go.mod go.sum ./
COPY internal/sandboxclient internal/sandboxclient
COPY cmd/sandboxd cmd/sandboxd
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/sandboxd ./cmd/sandboxd

FROM node:24-bookworm-slim

ARG CLAUDE_CODE_VERSION
ARG CODEX_VERSION
RUN test -n "$CLAUDE_CODE_VERSION" && test -n "$CODEX_VERSION" \
 || { echo "CLAUDE_CODE_VERSION and CODEX_VERSION are required (pinned in mise.toml)" >&2; exit 1; }

RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates jq ripgrep python3 procps \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} @openai/codex@${CODEX_VERSION} \
 && npm cache clean --force

# yq (mikefarah; Debian's yq package is a different tool) from a pinned release, verified against
# the published SHA-256 checksum.
ARG TARGETARCH
ARG YQ_VERSION=4.54.1
ARG YQ_SHA256_amd64=8e34fc298390875de416e6a4afcb8cabeceb25d9aa8506c1a2f9353cf702ea5f
ARG YQ_SHA256_arm64=189088da0c6429ec5178dfaab1a114805f6cab0b61b165ab236efedf1d57a71b
RUN set -eu; \
    case "$TARGETARCH" in amd64) yq_sha=$YQ_SHA256_amd64 ;; \
                          arm64) yq_sha=$YQ_SHA256_arm64 ;; \
                          *) echo "unsupported arch $TARGETARCH" >&2; exit 1 ;; esac; \
    curl -fsSLo /tmp/yq "https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_${TARGETARCH}"; \
    echo "$yq_sha  /tmp/yq" | sha256sum -c -; \
    install -m 0755 /tmp/yq /usr/local/bin/yq; \
    rm /tmp/yq

# The base image's "node" user holds uid/gid 1000, which the contract assigns to "agent".
RUN userdel --remove node \
 && groupadd --gid 1000 agent \
 && useradd --uid 1000 --gid 1000 --create-home --home-dir /home/agent --shell /bin/bash agent \
 && install -d -o agent -g agent /home/agent/work

COPY images/agent/managed-settings.json /etc/claude-code/managed-settings.json
COPY --from=sandboxd /out/sandboxd /usr/local/bin/sandboxd

USER agent
ENV HOME=/home/agent
WORKDIR /home/agent/work
EXPOSE 7777
ENTRYPOINT ["/usr/local/bin/sandboxd"]
