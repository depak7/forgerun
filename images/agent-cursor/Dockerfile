FROM node:22-bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates python3 python3-venv python3-pip \
 && rm -rf /var/lib/apt/lists/*

RUN curl https://cursor.com/install -fsS | bash

ENV PATH="/root/.local/bin:${PATH}"

WORKDIR /work