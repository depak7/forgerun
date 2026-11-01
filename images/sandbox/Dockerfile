# forgerun's general-purpose sandbox: a bare machine for people and their
# own agents to work in (clone, build, run tests). No coding agent inside;
# the agent images are separate.

# Node and Go come from their official images, so both architectures get
# the right binaries. Pinned to the versions forgerun was tested with; bump
# deliberately.
FROM node:22-bookworm-slim AS node
FROM golang:1.27.3-bookworm AS go

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl wget git openssh-client \
      build-essential pkg-config \
      python3 python3-pip python3-venv python-is-python3 \
      jq ripgrep unzip zip xz-utils tar procps less vim-tiny \
 && rm -rf /var/lib/apt/lists/*

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
 && ln -s ../lib/node_modules/corepack/dist/corepack.js /usr/local/bin/corepack

COPY --from=go /usr/local/go /usr/local/go
ENV GOPATH=/root/go
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH

WORKDIR /work
