FROM node:24-bookworm-slim

ARG CODEX_VERSION=0.162.1
RUN npm install -g @openai/codex@${CODEX_VERSION} \
 && apt-get update && apt-get install -y --no-install-recommends curl ca-certificates git \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --shell /bin/bash agent

USER agent
WORKDIR /home/agent
ENTRYPOINT ["sleep", "infinity"]
