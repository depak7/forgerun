FROM node:22-bookworm-slim

# fd and ripgrep: pi's find and grep tools run them, and would otherwise
# download the latest release from GitHub when first used.
RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates ripgrep fd-find \
 && rm -rf /var/lib/apt/lists/*

# Pinned to the version forgerun was tested with, with its whole dependency
# tree locked (package-lock.json); bump deliberately, audit first. No
# install scripts run: pi needs none (its dependencies' are a no-op and a
# notice).
WORKDIR /opt/pi
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund \
 && npm cache clean --force \
 && ln -s /opt/pi/node_modules/.bin/pi /usr/local/bin/pi

WORKDIR /work
