FROM node:22-bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Pinned to the version forgerun was tested with; bump deliberately.
RUN npm install -g @openai/codex@0.167.0 \
 && npm cache clean --force

WORKDIR /work
