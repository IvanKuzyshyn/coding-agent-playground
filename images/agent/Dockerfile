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

# gh and yq (mikefarah; Debian's yq package is a different tool) from pinned releases, verified
# against the vendors' published SHA-256 checksums.
ARG TARGETARCH
ARG GH_VERSION=2.102.0
ARG GH_SHA256_amd64=bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386
ARG GH_SHA256_arm64=7862c86c72f43df3a2d93ddde6f473285b4e2af61b494849846827e513ef6484
ARG YQ_VERSION=4.54.1
ARG YQ_SHA256_amd64=8e34fc298390875de416e6a4afcb8cabeceb25d9aa8506c1a2f9353cf702ea5f
ARG YQ_SHA256_arm64=189088da0c6429ec5178dfaab1a114805f6cab0b61b165ab236efedf1d57a71b
RUN set -eu; \
    case "$TARGETARCH" in amd64) gh_sha=$GH_SHA256_amd64 yq_sha=$YQ_SHA256_amd64 ;; \
                          arm64) gh_sha=$GH_SHA256_arm64 yq_sha=$YQ_SHA256_arm64 ;; \
                          *) echo "unsupported arch $TARGETARCH" >&2; exit 1 ;; esac; \
    cd /tmp; \
    curl -fsSLo gh.tar.gz "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    echo "$gh_sha  gh.tar.gz" | sha256sum -c -; \
    tar -xzf gh.tar.gz; \
    install -m 0755 "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /usr/local/bin/gh; \
    curl -fsSLo yq "https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_${TARGETARCH}"; \
    echo "$yq_sha  yq" | sha256sum -c -; \
    install -m 0755 yq /usr/local/bin/yq; \
    rm -rf /tmp/gh.tar.gz /tmp/gh_* /tmp/yq

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
