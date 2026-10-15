FROM node:22-bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Pinned to the version forgerun was tested with; bump deliberately.
RUN npm install -g @anthropic-ai/claude-code@2.1.300 \
 && npm cache clean --force

WORKDIR /work
