# The orchestrator: forgerun server (web UI, API, sign-in, pushing). It runs
# no VMs, so it needs no virtualization; workers run elsewhere with
# forgerun worker. Build: docker build -t forgerun .
FROM node:22-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
# cgo: the microsandbox SDK loads its library at run time (workers only).
RUN CGO_ENABLED=1 go build -tags webui -trimpath -ldflags="-s -w" -o /forgerun ./cmd/forgerun

FROM debian:bookworm-slim
# git pushes branches; ca-certificates for GitHub over HTTPS.
# The log folder is writable by any user: some platforms run the container
# as their own user, not this one.
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --uid 10001 forgerun \
 && mkdir -p /home/forgerun/out && chmod 1777 /home/forgerun/out
COPY --from=go /forgerun /usr/local/bin/forgerun
USER forgerun
WORKDIR /home/forgerun
# It listens on 0.0.0.0:$PORT; platforms that assign a port override it.
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["forgerun"]
CMD ["server", "-out", "/home/forgerun/out"]
