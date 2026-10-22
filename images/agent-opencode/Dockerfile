FROM node:22-bookworm-slim

# ripgrep: OpenCode's grep tool runs rg.
RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates ripgrep \
 && rm -rf /var/lib/apt/lists/*

# Pinned to the version forgerun was tested with; bump deliberately, and the
# plugin package with it: OpenCode installs it into its config dir on its
# first run (63 MB), so it's done here instead of in every task.
RUN npm install -g opencode-ai@1.19.3 \
 && mkdir -p /root/.config/opencode \
 && cd /root/.config/opencode \
 && npm install --no-fund --no-audit @opencode-ai/plugin@1.19.3 \
 && npm cache clean --force

WORKDIR /work
