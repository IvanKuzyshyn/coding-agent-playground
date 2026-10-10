FROM node:24-bookworm-slim

ARG CLAUDE_CODE_VERSION=2.1.296
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
 && apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --shell /bin/bash agent

USER agent
WORKDIR /home/agent
ENTRYPOINT ["sleep", "infinity"]
