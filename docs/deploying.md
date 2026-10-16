# Deploying forgerun for other people

The shape that works best: the **orchestrator** on a small server with a domain and
HTTPS, and one or more **workers** on machines with virtualization (your Mac is fine).
Workers dial out to the orchestrator, so nothing needs opening on them, and turns wait in
the queue while no worker is connected.

```
browsers ──HTTPS──▶  forgerun server  (VPS: Caddy · forgerun · Postgres)
                          ▲
                          └── HTTPS, outbound ──  forgerun worker  (your Mac: microVMs)
```

## 1. The orchestrator on a server

Any small Linux VPS with Docker works (1 vCPU, 1 GB is plenty: it runs no VMs). Point a
DNS name at it, for example `forgerun.example.com`, and open ports 80 and 443.

**Create a GitHub OAuth App** (github.com → Settings → Developer settings → OAuth Apps →
New OAuth App):

| Field | Value |
|---|---|
| Homepage URL | `https://forgerun.example.com` |
| Authorization callback URL | `https://forgerun.example.com/auth/github/callback` |

Generate a client secret. Then, on the server:

```sh
git clone https://github.com/depak7/forgerun && cd forgerun/deploy
cp .env.example .env && chmod 600 .env    # fill it in: domain, secrets, OAuth app, admins
docker compose up -d --build
docker compose logs -f forgerun           # "forgerun orchestrator serving"
```

Caddy gets the HTTPS certificate by itself. Open `https://forgerun.example.com`, sign in
with GitHub, and you're the admin (your username is in `FORGERUN_ADMINS`). Settings shows
"No worker is connected" until step 2.

## 2. A worker on your Mac

The worker needs macOS on Apple Silicon or Linux with KVM, git, and the agent keys:

```sh
make build                                  # builds ./forgerun
cat > ~/.forgerun-worker.env <<'ENV'
FORGERUN_SERVER=https://forgerun.example.com
FORGERUN_WORKER_TOKEN=…                     # the same value as on the server
CURSOR_API_KEY=…
CLAUDE_CODE_OAUTH_TOKEN=…                   # from: claude setup-token
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

## 3. Before you invite people

- **Limits.** Testers run on your agent keys. The defaults are 1 turn waiting or running
  per person and 20 a day; change them in `deploy/docker-compose.yml`'s `command`
  (`-user-live`, `-user-turns-per-day`). Admins aren't limited.
- **Pause switch.** Settings → Usage → `[ pause new tasks ]` stops new work for everyone
  but admins, at once; running turns carry on.
- **What testers grant.** Signing in asks GitHub for `repo` (their repositories, to clone
  private ones and push branches and pull requests as them) and `read:user`. Their tokens
  are stored encrypted with `FORGERUN_SECRET_KEY`; keep that key, or everyone must sign in
  again. They can revoke access in GitHub → Settings → Applications.
- **What a VM can reach.** Agents run arbitrary code inside their microVM, with network
  access. They never see API keys or GitHub tokens: agent keys are swapped in by a proxy,
  and private repos are fetched on the worker's host and copied in.
- **Secrets to guard:** `.env` on the server (OAuth secret, `FORGERUN_SECRET_KEY`,
  `GITHUB_TOKEN`, `FORGERUN_WORKER_TOKEN`) and the worker's env file (agent keys, worker
  token). Anyone with the worker token can pretend to be a worker and receive turns
  (including private repos' fetch tokens): rotate it if it leaks.

## All on one machine instead

`forgerun serve` runs the orchestrator and the VMs in one process. To share it from your
Mac without a server, put HTTPS in front of it with a **named** tunnel (a quick tunnel's
address changes every run, and the OAuth callback must stay the same):

```sh
brew install cloudflared
cloudflared tunnel login && cloudflared tunnel create forgerun
cloudflared tunnel route dns forgerun forgerun.example.com
cloudflared tunnel run --url http://localhost:8080 forgerun
```

Set `FORGERUN_PUBLIC_URL=https://forgerun.example.com` and the OAuth App's URLs to match.
Tailscale Funnel works the same way if you'd rather not use a domain of your own.
