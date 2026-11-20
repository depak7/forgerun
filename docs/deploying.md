# Deploying forgerun

Two ways: everything on one machine, or the orchestrator on a server with workers on
other machines.

## All on one machine

`forgerun serve` runs the orchestrator and the VMs in one process. To reach it from other
devices, set your sign-in in its `.env`. It won't listen beyond `127.0.0.1` without it, but
a tunnel reaches it on `127.0.0.1`, so behind one it can't tell: always set it.

```sh
FORGERUN_USERNAME=…
FORGERUN_PASSWORD=…                         # at least 12 characters
FORGERUN_PUBLIC_URL=https://forgerun.example.com
```

To reach it only from your own devices, put the machine on Tailscale and skip the
tunnel below. To share it from your Mac without a server, put HTTPS in front of it with a
**named** tunnel (a quick tunnel's address changes every run):

```sh
brew install cloudflared
cloudflared tunnel login && cloudflared tunnel create forgerun
cloudflared tunnel route dns forgerun forgerun.example.com
cloudflared tunnel run --url http://localhost:8080 forgerun
```

## The orchestrator on a server, workers elsewhere

The **orchestrator** runs on a small server with a domain and HTTPS, and one or more
**workers** on machines with virtualization (your Mac is fine). Workers dial out to the
orchestrator, so nothing needs opening on them, and turns wait in the queue while no
worker is connected.

```
browsers ──HTTPS──▶  forgerun server  (VPS: Caddy · forgerun · Postgres)
                          ▲
                          └── HTTPS, outbound ──  forgerun worker  (your Mac: microVMs)
```

### 1. The orchestrator

Any small Linux VPS with Docker works (1 vCPU, 1 GB is plenty: it runs no VMs). Point a
DNS name at it, for example `forgerun.example.com`, and open ports 80 and 443.

On the server:

```sh
git clone https://github.com/depak7/forgerun && cd forgerun/deploy
cp .env.example .env && chmod 600 .env    # fill it in: domain, secrets, your username and password
docker compose up -d --build
docker compose logs -f forgerun           # "forgerun orchestrator serving"
```

Caddy gets the HTTPS certificate by itself. Open `https://forgerun.example.com` and sign in
with `FORGERUN_USERNAME` and `FORGERUN_PASSWORD`. Settings shows "No worker is connected"
until step 2.

### 2. A worker on your Mac or a Linux machine

The worker needs a Mac with Apple Silicon or a Linux machine. Linux with KVM (`/dev/kvm`)
runs microVMs; any other Linux machine, such as an ordinary cloud VM, runs gVisor
sandboxes, which the installer sets up (Docker and gVisor, with sudo). On it, run the installer and
choose **worker**; it asks for the orchestrator's address, the worker token (the server's
`FORGERUN_WORKER_TOKEN`) and your agent keys, checks the setup with `forgerun doctor`, and
runs the worker in the background, now and at every boot:

```sh
curl -fsSL https://raw.githubusercontent.com/depak7/forgerun/main/install.sh | sh
```

Without a terminal, answer ahead:

```sh
FORGERUN_ROLE=worker FORGERUN_SERVER=https://… FORGERUN_WORKER_TOKEN=… sh install.sh
```

Its settings are in `~/.forgerun-worker.env`.

Or by hand:

```sh
make build                                  # builds ./forgerun
cat > ~/.forgerun-worker.env <<'ENV'
FORGERUN_SERVER=https://forgerun.example.com
FORGERUN_WORKER_TOKEN=…                     # the same value as on the server
CURSOR_API_KEY=…
CLAUDE_CODE_OAUTH_TOKEN=…                   # from: claude setup-token
OPENCODE_API_KEY=…                          # optional: OpenCode's free models need none
OPENROUTER_API_KEY=…                        # pi: one key per provider you'll use (or OPENAI_/ANTHROPIC_API_KEY)
ENV
chmod 600 ~/.forgerun-worker.env
set -a && . ~/.forgerun-worker.env && set +a && caffeinate -i ./forgerun worker
```

`caffeinate -i` keeps the Mac awake while the worker runs. The worker logs "connected to
the orchestrator", and Settings lists it with the agents it has keys for. Ctrl-C stops it:
a turn still running is stopped and marked failed (send the message again to retry), each
open session is saved as a warm snapshot, and nothing already done is lost. Turns started
while it's away wait in the queue for it.

More workers can connect with the same token; each takes turns from the queue, and a
thread sticks to the worker holding its VM.

### 3. Keeping it to yourself

- **Sign-in.** forgerun has one owner: `FORGERUN_USERNAME` and `FORGERUN_PASSWORD` (at
  least 12 characters). It won't listen on an address other machines can reach without
  them. A browser login lasts 30 days, or until you sign out. After 5 failed sign-ins in
  a row, an address waits 15 minutes; behind Caddy every request comes from Caddy, so the
  wait applies to everyone, you included: use a long random password.
- **Tools.** For `forgerun mcp` and scripts, make a personal token in Settings and pass it
  as `FORGERUN_TOKEN`. Revoke one there and it stops working at once.
- **Fewer addresses.** `FORGERUN_ALLOW_FROM` (IPs or ranges, comma-separated) refuses
  every other address before sign-in. It sees the address that connects to forgerun, so
  behind a proxy (Caddy here) it sees only the proxy: limit addresses in the proxy
  instead. Simplest of all is no public address: put the server on a private network
  such as Tailscale and set `FORGERUN_ALLOW_FROM=100.64.0.0/10`.
- **What a VM can reach.** Agents run arbitrary code inside their microVM, with network
  access. They never see API keys or GitHub tokens: agent keys are swapped in by a proxy,
  and private repos are fetched on the worker's host and copied in.
- **Secrets to guard:** `.env` on the server (`FORGERUN_PASSWORD`, `GITHUB_TOKEN`,
  `FORGERUN_WORKER_TOKEN`) and the worker's env file (agent keys, worker
  token). Anyone with the worker token can pretend to be a worker and receive turns
  (including private repos' fetch tokens): rotate it if it leaks.

## A shell on a worker

Workers only dial out, so nothing on them accepts connections, SSH included. To reach a
worker at home, put it on a private network you control (a VPN such as WireGuard or a mesh
VPN) and SSH in over that; don't open SSH on your router. Give that access only to whoever
runs the worker: the host holds the agent keys and every thread's VM.

On the worker, a thread's sandbox can be opened directly:

```sh
msb ls                          # microVMs: a worker's are named fw-…, serve's fs-…
msb exec fw-… -- sh
docker ps                       # gVisor sandboxes
docker exec -it <container> sh
```

A sandbox holds no keys, only placeholders. Anything you change in its repository becomes
part of the thread's next turn, and can be pushed with it. An idle session's VM is saved
and closed after `-session-idle` (30 minutes by default).

## The orchestrator on a container platform

Any platform that builds and runs a Dockerfile works for the orchestrator (it runs no VMs),
with a managed Postgres from the same platform or elsewhere. Build from the repo's
`Dockerfile`; the image runs `forgerun server`. Set:

| Variable | |
|---|---|
| `FORGERUN_DATABASE_URL` (or `DATABASE_URL`) | the Postgres URL, e.g. `…?sslmode=require`. With a connection pooler in front, use its direct (non-pooled) address. |
| `FORGERUN_PUBLIC_URL` | the app's `https://…` address |
| `FORGERUN_WORKER_TOKEN` | `openssl rand -hex 32` |
| `GITHUB_TOKEN`, `FORGERUN_USERNAME`, `FORGERUN_PASSWORD` | as in step 1 |
| `PORT` | usually set by the platform; forgerun listens on `0.0.0.0:$PORT` (8080 if unset) |

The migrations run on the first start. Workers connect as in step 2, with
`FORGERUN_SERVER` set to the app's address.

- **Run exactly one instance.** The orchestrator keeps the connected workers in memory.
- **Disk:** turn logs are files under `/home/forgerun/out`. On platforms whose disk is
  wiped on restart, finished turns' logs are lost then (their diffs, replies and threads
  are in Postgres); mount a volume there to keep them.
- **Sleeping instances:** a connected worker reports in every 2 seconds, which counts as
  traffic. On a database that bills compute time, note that the orchestrator checks the
  queue every few seconds while a worker waits for work.
