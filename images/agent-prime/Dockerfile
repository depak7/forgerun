FROM python:3.11-slim-bookworm

# git/curl/ca-certificates: clone and HTTPS. ripgrep / fd: useful from the
# Python REPL. The REPL needs prime-agent-runtime (ships in the release
# tarball) plus ipykernel — shipping both avoids a per-task bootstrap into
# ~/.prime and ~/.local.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git ripgrep fd-find \
 && rm -rf /var/lib/apt/lists/*

# Pinned release; bump deliberately and update the checksums together.
# https://github.com/PrimeIntellect-ai/prime-agent/releases/tag/v0.10.3
ARG PRIME_VERSION=0.10.3
ARG TARGETARCH
RUN set -eu; \
  case "$TARGETARCH" in \
    amd64) arch=linux-x64;  sha=811d9f0ec9dff007d5bda9c8435b0e7f46ef7041fa2fc103efc9edc295cb5528 ;; \
    arm64) arch=linux-arm64; sha=a41da67f91897bc32a630fac65cc141bf83e1f2cb91ec133ef9194e2e8a99926 ;; \
    *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
  esac; \
  file="prime-agent-${PRIME_VERSION}-${arch}.tar.gz"; \
  curl -fsSL -o "/tmp/$file" \
    "https://github.com/PrimeIntellect-ai/prime-agent/releases/download/v${PRIME_VERSION}/$file"; \
  echo "$sha  /tmp/$file" | sha256sum -c -; \
  mkdir -p /opt/prime-agent; \
  tar -xzf "/tmp/$file" -C /opt/prime-agent; \
  ln -s /opt/prime-agent/prime-agent /usr/local/bin/prime-agent; \
  rm -f "/tmp/$file"; \
  python3 -m venv /opt/prime-kernel; \
  /opt/prime-kernel/bin/pip install --no-cache-dir \
    ipykernel \
    "/opt/prime-agent/prime-agent-runtime" \
    requests httpx pyyaml tomli python-dotenv \
    pandas numpy scipy beautifulsoup4 lxml; \
  for d in /opt/prime-agent/skills/*/pyproject.toml; do \
    /opt/prime-kernel/bin/pip install --no-cache-dir "$(dirname "$d")"; \
  done

ENV PRIME_AGENT_KERNEL_PYTHON=/opt/prime-kernel/bin/python \
    PI_OFFLINE=1 \
    PRIME_AGENT_TELEMETRY=0 \
    DO_NOT_TRACK=1

WORKDIR /work
