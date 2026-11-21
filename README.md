# forgerun

[![License: Apache-2.0](https://img.shields.io/github/license/depak7/forgerun)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/depak7/forgerun)](go.mod)
[![GitHub stars](https://img.shields.io/github/stars/depak7/forgerun?style=flat)](https://github.com/depak7/forgerun/stargazers)
[![Website](https://img.shields.io/badge/website-forgerun.depak.dev-4ADE9B)](https://forgerun.depak.dev)

**Turn machines you already own into managed sandboxes for coding agents.**
Start threads on Claude Code, Cursor CLI, OpenCode, pi or Prime Agent from the web UI, or from another agent over MCP (`forgerun mcp`).
Each thread runs end to end in its own isolated microVM ([microsandbox](https://github.com/superradcompany/microsandbox)) or [gVisor](https://gvisor.dev) sandbox on your Mac or Linux box; you review the diff and push a branch or PR as you, only if you want it.

<p align="center">
  <a href="#quick-start">Quick Start</a> ·
  <a href="https://forgerun.depak.dev">Website</a> ·
  <a href="#how-it-works">How It Works</a> ·
  <a href="#how-it-compares">Comparison</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="docs/deploying.md">Deploying</a> ·
  <a href="docs/experiments.md">Benchmarks</a> ·
  <a href="#faq">FAQ</a> ·
  <a href="llms.txt">llms.txt</a>
</p>

![forgerun: pick a repo, a branch and an agent; approve its plan; the agent builds and tests in its own sandbox; review the diff before anything is pushed](docs/demo.gif)

- **26.8 s → 2.8 s warm starts.** Automatic repo disk snapshots keep dependencies pre-installed and self-clean within your disk budget (measured on `expressjs/express`: [docs/experiments.md](docs/experiments.md#4-snapshots-for-fast-task-start)).
- **Keys never enter the sandbox.** The VM sees only placeholders; a host TLS proxy swaps in the real key exclusively for the agent's official API.
- **Queue, hand off or orchestrate.** Queue as many tasks as you want, switch agents mid-thread, hand off local uncommitted work, or let another agent start and review threads via `forgerun mcp`.
- **Workers dial out over HTTPS only.** Run the web UI on a small VPS and the VMs on your Mac or home Linux machine with zero open inbound ports.

## Quick start

You need a Mac with Apple Silicon, or Linux: with KVM (`ls /dev/kvm`) for microVMs, or any
Linux machine with [gVisor](https://gvisor.dev), which the installer sets up. Then:

```sh
curl -fsSL https://raw.githubusercontent.com/depak7/forgerun/main/install.sh | sh
```

![Installing a worker on an EC2 VM without KVM: the installer sets up gVisor, builds forgerun, checks it with forgerun doctor and runs it under systemd](docs/quick-start.gif)

It asks what the machine is for (a **worker** for an orchestrator elsewhere, or
**everything** on this machine) and for any keys, then does the rest by itself: installs
what's missing (git, a C compiler, Go; Node for the web UI), builds forgerun into
`~/forgerun`, checks the setup with `forgerun doctor`, and runs it in the background
(systemd on Linux, launchd on macOS), now and at every boot. Run it again to update.

`forgerun doctor` checks a machine's setup any time and says how to fix what's wrong:
virtualization (KVM, `kvm` group, a VM without nested virtualization) or Docker with gVisor,
the microsandbox runtime and its database, disk and memory, image downloads, the orchestrator and worker
token, Postgres, `GITHUB_TOKEN` and the sign-in settings. It boots one microVM to time a
cold start (`-quick` skips that).

```sh
~/forgerun/forgerun doctor -env ~/.forgerun-worker.env   # a worker's settings
```

To set it up step by step instead, see [By hand](#by-hand).

## How it works

forgerun is a single Go binary with a web UI. Agents are pluggable adapters
([`internal/agent`](internal/agent)).

- **Isolated:** every thread gets its own sandbox: a microVM where there's KVM or Apple
  Silicon, otherwise a gVisor sandbox (a container run by gVisor's own user-space kernel,
  so the agent never talks to the host's). The agent can install, build and test whatever
  it needs (the images have git, Node and Python). Pick one with `-sandbox microsandbox|gvisor`; the default (`auto`) uses
  microVMs where they can run.
- **A live session per thread:** the VM stays up between your messages, so the agent keeps
  its own conversation and working tree.
- **Review, then push:** each turn's changes are kept as a diff. Nothing reaches GitHub
  until you press push — then it's one commit on a readable `forgerun/…` branch, with a
  pull request if you want one.
- **Any branch:** a thread starts from the repo's default branch or one you pick, and its
  pull request goes into that branch.
- **Warm starts:** when a session closes, its VM is saved as the repo's snapshot; the next
  thread on that repo starts from it in seconds.
- **Drive it from another agent:** `forgerun mcp` lets Claude Code, Cursor or any MCP agent
  start threads, follow and review them, and push the result
  ([below](#drive-forgerun-from-another-agent-mcp)).

**Built on [microsandbox](https://github.com/superradcompany/microsandbox)** (libkrun
microVMs) and [gVisor](https://gvisor.dev). forgerun isn't a sandbox provider: it's what
runs around the agents (sessions, review, pushing, workers, sign-in). With gVisor, which
has no key proxy of its own, forgerun's (`internal/secretproxy`) keeps keys out.

> Status: early. It works end to end (sign-in, MCP, workers on other machines), and
> things still change quickly.

## How it compares

When you want to run cloud coding agents end to end off your laptop, here is how forgerun compares to the main alternatives:

| Setup | Where control plane & sandboxes run | API keys & GitHub push | Queue, handoff & snapshots | Agents & cost |
|---|---|---|---|---|
| **forgerun (machines you already own)** | **100% self-hosted** on your Apple Silicon Mac, Linux KVM box, or Linux VPS via `gVisor` (0.34 s cold boot); managed automatically with outbound HTTPS workers | **Never inside the sandbox:** brokered placeholders swapped by a host TLS proxy; host pushes **as you** only when you click push | Unlimited Postgres task queue; **mid-task local → cloud handoff** (`forgerun mcp`); automatic rolling disk snapshots (**2.8 s** warm starts) | `claude-code` (Pro/Max), `cursor`, `opencode` (free or paid), `pi`, `prime` — switch agents mid-thread; **open source (Apache-2.0)** |
| **Vendor cloud coding agents** (Cursor Cloud Agents, Claude Code on the web, OpenAI Codex Cloud, GitHub Copilot coding agent, Devin) | Vendor-managed cloud VMs (shared host kernel on self-hosted runners) | Managed on vendor cloud; env secrets often visible to the agent or left to you on self-hosted runners | Vendor web/mobile/editor tasks and cloud environments | Locked to one vendor's agent and seat/usage billing |
| **Single-agent open-source runners** (OpenHands / OpenDevin) | Docker containers on your machine or vendor cloud | API keys typically passed via config or container environment | Web UI for its own built-in agent loop; cold container workspace per conversation | Runs OpenHands' own agent rather than orchestrating Claude Code, Cursor CLI, OpenCode, pi, and Prime side by side |
| **Cloud sandbox providers & SDKs** (E2B, Daytona, Modal, Fly.io Sprites, `microsandbox`) | Paid cloud microVMs or low-level runtime SDK | Varies (`microsandbox` brokers secrets; cloud sandboxes typically use env vars unless you add a proxy) | Low-level primitive: you build the task queue, web UI, sessions, diff review, PR flow, and workers yourself *(forgerun is built on `microsandbox` & `gVisor`)* | Per-second cloud VM billing or DIY orchestration stack |
| **Local CLI or `tmux` / plain Docker on a VPS** | Shares your laptop or shares the host Linux kernel; DIY VM/container scripts | Local `~/.ssh`, `.env`, or exported env vars readable by prompt injection or `postinstall` scripts | Ties up your local branch, or cold ~27 s clone + install with no built-in queue or diff gate | Your own subscriptions/keys; DIY scripts for logs, sessions, and PRs |

## Architecture

```mermaid
flowchart LR
    subgraph Browser
        UI["Web UI<br/>(React, embedded in the binary)"]
    end

    subgraph Host["forgerun serve (one Go binary)"]
        API["HTTP API<br/>/api/…"]
        Pool["Worker pool<br/>claims queued turns"]
        Runner["Runner<br/>runs a turn in the thread's session"]
        Sessions["Session registry<br/>open VMs · idle timeout · cap"]
        Snap["Snapshot manager<br/>warm starts · cleanup"]
        Pub["Publisher<br/>git push · pull requests"]
        Proxy["Secrets proxy<br/>(microsandbox's, or forgerun's for gVisor)"]
    end

    subgraph VM["microVM (or gVisor sandbox) per thread"]
        Agent["Agent CLI<br/>Claude Code · Cursor · OpenCode · pi · Prime"]
        Repo["/work/repo<br/>one commit per turn"]
    end

    DB[("Postgres<br/>tasks · diffs · replies · snapshots")]
    GH["GitHub"]
    LLM["Agent APIs<br/>cursor.sh · anthropic.com"]

    UI <--> API
    API --> DB
    Pool --> DB
    Pool --> Runner
    Runner <--> Sessions
    Runner --> Snap
    Sessions --> VM
    Agent --> Repo
    Agent -- "placeholder key" --> Proxy -- "real key" --> LLM
    API -- "push" --> Pub --> GH
```

### A thread, step by step

```mermaid
sequenceDiagram
    actor You
    participant UI as Web UI
    participant API
    participant W as Worker
    participant VM as Session VM
    participant GH as GitHub

    You->>UI: New task (repo, prompt, agent, model)
    UI->>API: POST /api/tasks
    W->>VM: open session (warm snapshot or clone)
    W->>VM: run agent, commit the turn
    W->>API: store diff + reply
    You->>UI: "Rename it to say_hello"
    W->>VM: same VM, agent continues its conversation
    You->>UI: push & open PR
    API->>VM: ask the agent for a summary
    API->>GH: one squashed commit on forgerun/…, open the PR
    Note over VM: after 30 idle minutes the VM is saved<br/>as the repo's snapshot and closed
```

- **Tasks are turns.** Every message in a thread is a task row: its prompt, the
  agent's reply, its diff, and its log.
- **Sessions are in memory; work is in the database.** If a session closes
  (idle, server restart, crash), the next message reopens it: a new VM at the
  thread's start commit, each earlier turn's diff re-applied, and the
  conversation given to the agent as text.
- **Agents are adapters.** An agent says which image it runs in, which
  credential it needs and for which hosts, and how to run and continue its CLI
  ([`internal/agent`](internal/agent)). Models are rows in the database.

## By hand

You need: macOS on Apple Silicon or Linux with KVM (for microVMs), Go 1.26,
Node 22, Docker (for Postgres), and git.

```sh
git clone https://github.com/depak7/forgerun && cd forgerun
docker compose up -d                 # Postgres on 127.0.0.1:5433
cp .env.example .env                 # then fill it in (see below)
make build                           # builds the UI and ./forgerun
set -a && source .env && set +a && ./forgerun serve
```

Open <http://localhost:8080>. The microsandbox runtime installs itself on the
first start; the agent images are pulled the first time an agent runs.

### Keys (`.env`)

| Variable | What for |
|---|---|
| `GITHUB_TOKEN` | Required. A fine-grained token with *Contents* and *Pull requests* read/write on the repos you'll use. |
| `CURSOR_API_KEY` | For the `cursor` agent. |
| `CLAUDE_CODE_OAUTH_TOKEN` | For `claude-code`; the output of `claude setup-token` (Pro/Max subscription). |
| `OPENCODE_API_KEY` | Optional, for `opencode`'s paid models: a key from an [OpenCode Console](https://opencode.ai/console) service account ("Inference only"). Its free models need no key. |
| `OPENROUTER_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` | For `pi` and `prime`: the API key of each provider whose models you'll pick (an `openrouter/…` model needs `OPENROUTER_API_KEY`, and so on). Subscription logins can't be used. Each key is sent only to its provider's API. |
| `GEMINI_API_KEY`, `XAI_API_KEY`, `PRIME_API_KEY` | For `prime` only: Google Gemini, xAI, and Prime Inference. |

An agent without its key is still listed; only its tasks are refused, except on models that need no key.

### Claude Code on your own subscription

`claude-code` runs on your Claude Pro or Max plan, so its turns count against your
subscription's limits instead of API billing.

1. On any machine with [Claude Code](https://code.claude.com/docs) installed, run
   `claude setup-token`. It opens your browser to sign in to your Claude account, then prints a token.
2. Paste it where the agents run:
   - **Installing:** the installer asks for `CLAUDE_CODE_OAUTH_TOKEN`; paste it there (it isn't shown as you type).
   - **Already installed:** add `CLAUDE_CODE_OAUTH_TOKEN=<the token>` to the env file (`~/.forgerun-worker.env`
     on a worker, `~/forgerun/.env` for everything on one machine), then run the install command again: it keeps
     your settings and restarts forgerun.
3. Settings in the web UI now shows `claude-code` as `SET`.

The token never enters the sandbox: the agent sees a placeholder, and the proxy on the host adds the real token
only for Anthropic's API. It's your personal subscription, so use it for your own tasks, not as a shared key for
other people. If it leaks, revoke it from your Claude account and make a new one.

**Sign-in.** forgerun has one owner. On `127.0.0.1` (the default) sign-in is optional:
without it, forgerun runs in local mode. On an address other machines can reach, it won't
start without it.

| Variable | What for |
|---|---|
| `FORGERUN_USERNAME`, `FORGERUN_PASSWORD` | your sign-in (the password: at least 12 characters). A browser login lasts 30 days; after 5 failed sign-ins an address waits 15 minutes |
| `FORGERUN_ALLOW_FROM` | only these addresses may connect, e.g. `127.0.0.1,192.168.1.0/24`. Unset: any, behind sign-in |
| `FORGERUN_PUBLIC_URL` | where browsers reach forgerun, e.g. `https://forgerun.example.com` |
| `FORGERUN_LANDING_ONLY` | `true` serves only the public landing page: no API, no sign-in, no database needed |
| `FORGERUN_ANALYTICS_WEBSITE_ID` | a website ID for the analytics script at `cloud.umami.is`: the pages load it. Unset (the default), they load no analytics |

### Useful `serve` flags

| Flag | Default | |
|---|---|---|
| `-workers` | `2` | turns that may run at once |
| `-session-idle` | `30m` | close a session after this long without a message |
| `-sessions` | `4` | most open sessions; the least recently used idle one closes first |
| `-timeout` | `2h` | max time per turn (a stuck agent is stopped sooner: `-agent-idle`) |
| `-agent-idle` | `10m` | stop an agent that prints nothing and changes nothing |
| `-snapshot-budget` | `20GB` | disk for warm-start snapshots |
| `-require-zdr` | off | offer only zero-data-retention models |
| `-sandbox` | `auto` | `microsandbox` (microVMs), `gvisor` (Docker + gVisor), or `auto`: microVMs where they can run |

Run `./forgerun serve -h` for all of them.

## One machine, or orchestrator and workers apart

`forgerun serve` runs everything in one process: the web UI, the API and the VMs.
To run the VMs elsewhere (for example the orchestrator on a small VPS, the VMs on a
machine at home), use two roles of the same binary:

```sh
# the orchestrator: web UI, API, Postgres, sign-in, pushing. No virtualization needed.
FORGERUN_WORKER_TOKEN=… ./forgerun server

# a worker, on a Mac (Apple Silicon) or any Linux machine (KVM, or gVisor). It dials out over
# HTTPS, so nothing needs opening on its side. Agent keys live here, not on the server.
FORGERUN_WORKER_TOKEN=… CURSOR_API_KEY=… ./forgerun worker -server https://forgerun.example.com
```

```mermaid
flowchart LR
    subgraph O["forgerun server (any VPS)"]
        UI2["web UI · API"] --- Q[("Postgres queue")]
        Q --- P2["push to GitHub"]
    end
    subgraph W["forgerun worker (your machine)"]
        R2["runner · sessions"] --> V2["microVMs"]
    end
    W -- "claim turns · stream logs · heartbeat (HTTPS, outbound)" --> O
```

- **Turns wait in the queue** while no worker is connected, and start as soon as one is.
  Run several workers; each takes turns from the same queue.
- **A thread sticks to the worker holding its VM.** If that worker goes away, its running
  turn fails after 30 seconds and the next message rebuilds the session on any worker.
- **Secrets:** agent keys stay on the workers, users' GitHub tokens on the orchestrator.
  For a private repo, the turn's token goes to the worker in memory, to fetch the code on
  that host; it never enters a VM.
- Each worker keeps its own warm-start snapshots (`~/.forgerun/worker`), and removes VMs
  a crashed run left behind when it starts.
- Settings lists the connected workers.

To deploy it on a server (orchestrator with HTTPS and sign-in, a worker on your machine),
see [docs/deploying.md](docs/deploying.md): a Dockerfile and a compose file
with automatic HTTPS are included (`deploy/`).

## Drive forgerun from another agent (MCP)

`forgerun mcp` is an MCP server any agent can start (Claude Code, Cursor, Codex, or your own):
it can hand off what it's doing, or act as an orchestrator, starting threads on your repos,
following them, reviewing and steering them, and pushing the result. The agents do the coding
in forgerun's sandboxes; the one driving them never gets your keys or GitHub token.

| Tool | |
|---|---|
| `forgerun_agents` | the agents forgerun can run and whether each has its key; with `agent`, its models and efforts |
| `forgerun_branches` | a repo's default branch and its branches |
| `forgerun_start` | start a thread on a repo: prompt, optional branch, agent, model, effort, plan first |
| `forgerun_handoff` | send a copy of the local folder and start a thread whose first message is the agent's summary of the session (what you asked, what's done, what's left) |
| `forgerun_threads` | your threads, and which need attention |
| `forgerun_status` | how a thread is doing: running, finished, failed, or waiting for an answer or approval; `wait` returns once the turn finishes |
| `forgerun_log` | the end of the latest turn's log |
| `forgerun_diff` | the thread's changes as one patch |
| `forgerun_followup` | the next message: ask for changes, answer questions, or `approve` the plan; may switch agent or model |
| `forgerun_cancel` | stop the running turn |
| `forgerun_push` | push the thread's branch, and with `pr` open a pull request into the branch it started from |

A typical loop: `forgerun_start` with `plan`, `forgerun_status` with `wait`, `forgerun_followup`
with `approve`, `forgerun_status` with `wait` again, review `forgerun_diff`, a follow-up for
anything to fix, then `forgerun_push` with `pr`.

Add it to your agent, e.g. Claude Code:

```sh
claude mcp add forgerun --env FORGERUN_URL=https://forgerun.example.com --env FORGERUN_TOKEN=frt_… -- forgerun mcp
```

or in any MCP client's config:

```json
{"mcpServers": {"forgerun": {"command": "forgerun", "args": ["mcp"],
  "env": {"FORGERUN_URL": "https://forgerun.example.com", "FORGERUN_TOKEN": "frt_…"}}}}
```

`FORGERUN_URL` defaults to `http://localhost:8080`. On a server with sign-in, create the token in
**Settings → Personal tokens** (one per tool; revoke it there). A server in local mode needs none.
Then ask your agent to "hand the rest off to forgerun", or to "use forgerun to fix the failing tests in owner/repo and open a PR".

**What's sent.** In a git repo, the repo's top folder: its files as they are now, uncommitted work
included, but not what `.gitignore` ignores, and only the current commit of its history, without its
remotes, credentials or hooks. Secret files git doesn't track (`.env`, `.env.*` but `.example`,
keys like `*.pem` or `id_rsa`, `.npmrc`, `.netrc`) never leave your machine. A plain folder is sent
whole but for those; a single file works too. Uploads are capped (`serve -max-folder`, 50 MiB
compressed by default). Your folder is never changed: apply the diff when you want it.

**Pushing.** When the folder is a GitHub repo and the commit it's on is on GitHub, its thread can
be pushed like any other: a new `forgerun/…` branch on that commit with your uncommitted work and
then the agent's, and a pull request to the branch you were on. Unpushed local commits can't be
built on: push them first, or take the diff.

## API

The UI uses the same JSON API (`{"success": true, "data": …}`):

| | |
|---|---|
| `POST /api/tasks` | start a thread: `{repo, prompt, branch?, agent?, model?, effort?}` (`branch`: start from it, and its pull request goes into it; default: the repo's default branch), or `{folder, …}` for an uploaded folder |
| `GET /api/repos/branches?repo=…` | a repo's default branch and up to 100 of its branches |
| `POST /api/folders?name=…` | upload a folder (a gzipped tar) to start threads on; `&repo=…&base=…&branch=…` for a GitHub repo's |
| `GET`/`POST /api/tokens`, `DELETE /api/tokens/{id}` | personal tokens (from a browser login); send one as `Authorization: Bearer frt_…` |
| `POST /auth/login`, `/auth/logout` | sign a browser in (`{username, password}`) and out |
| `POST /api/tasks/{id}/followups` | the next message in a thread |
| `POST /api/tasks/{id}/cancel` | stop a queued or running turn |
| `GET /api/tasks/{id}/thread` | every turn of a thread |
| `GET /api/tasks/{id}/log`, `/diff` | a turn's live log (Range supported) and diff |
| `POST /api/threads/{id}/push` | push unpushed turns as one commit; `{"pr": true}` also opens a pull request |
| `GET /api/threads/{id}/session`, `POST …/end` | a thread's session, and closing it |
| `GET /api/threads`, `/api/agents`, `/api/models`, `/api/status`, `/api/snapshots`, `/api/repos`, `/api/workers` | lists for the UI |
| `/api/worker/…` | the worker protocol: claim, log, finish, heartbeat (worker token) |

## Development

```sh
go vet ./... && go test ./...                    # Go (DB and VM tests skip)
govulncheck ./...                                # known vulnerabilities in Go and dependencies
FORGERUN_TEST_DATABASE_URL='postgres://forgerun:forgerun@localhost:5433/forgerun_test?sslmode=disable' \
  go test ./internal/store/...                   # against Postgres (docker compose up -d first)
FORGERUN_INTEGRATION=1 go test ./internal/snapshots/   # boots real microVMs
cd web && npm test && npx tsc --noEmit           # web UI
make dev-web                                     # UI with live reload on :5173
```

```
cmd/forgerun/       serve (everything), server (orchestrator only), worker, doctor, run (one task, no database)
install.sh          the one-step installer (worker or everything on one machine)
internal/doctor/    forgerun doctor's checks
internal/agent/     agent adapters: Claude Code, Cursor, OpenCode, pi, Prime; provider keys
internal/models/    agents' models: families, efforts, resolving a choice to a model
internal/task/      the runner: sessions, turns, replay, branches
internal/sandbox/   the sandbox backends: microsandbox (microVMs) and gVisor (Docker + runsc)
internal/secretproxy/ forgerun's key-swapping proxy, for gVisor sandboxes
internal/snapshots/ warm-start snapshots and their cleanup
internal/publish/   pushing branches and pull requests (host-side git), fetching private repos
internal/forge/     the GitHub API: repos, branches, pull requests
internal/store/     Postgres (migrations) and an in-memory store for tests
internal/worker/    the worker pool
internal/remote/    workers on other machines: the orchestrator side and the worker side
internal/api/       HTTP handlers, sign-in and personal tokens
internal/handoff/   forgerun mcp's tools (handoff and orchestration) and their API client
internal/mcp/       a minimal MCP server (stdio)
internal/capacity/  what a machine can run: free CPUs and memory
internal/webui/     serving the built UI from the binary
web/                the React UI (embedded with -tags webui)
images/             agent VM images
docs/experiments.md what each experiment showed, and why things are the way they are
```

## FAQ

<details>
<summary><b>How is forgerun different from Cursor Cloud Agents, Claude Code on the web, Codex Cloud, Devin, or OpenHands?</b></summary>
<br />

Cursor Cloud Agents, Claude Code on the web, OpenAI Codex Cloud, GitHub Copilot coding agent, and Devin run in vendor-managed clouds and lock you to a single vendor's agent and billing. OpenHands is open source, but runs its own single built-in agent in Docker containers. [forgerun](https://github.com/depak7/forgerun) is **100% open source (Apache-2.0)** and turns machines you already own into managed `libkrun` microVM or `gVisor` sandboxes where you can queue tasks across Claude Code, Cursor CLI, OpenCode, pi, and Prime Agent in one place, hand off work mid-task via `forgerun mcp`, start in 2.8s from rolling snapshots, keep API keys out of the sandbox, and push PRs as you only when you want to.

</details>

<details>
<summary><b>How is forgerun different from cloud sandbox providers like E2B, Daytona, Modal, or Fly.io Sprites?</b></summary>
<br />

Those sell raw cloud sandbox VMs by the second (or provide a low-level SDK where you still have to build the agent runner yourself). forgerun uses your own Mac or Linux machines as the sandbox host (built on [microsandbox](https://github.com/superradcompany/microsandbox) and [gVisor](https://gvisor.dev)) and ships the complete end-to-end coding-agent platform out of the box: Postgres task queue, five agent adapters, automatic rolling repo snapshots, a diff-review web UI, host-side branch and PR publishing, GitHub OAuth, outbound remote workers, and `forgerun mcp`.

</details>

<details>
<summary><b>Can I queue multiple coding-agent tasks at once across different repos and agents?</b></summary>
<br />

Yes. Tasks are queued in Postgres, so you can queue as many tasks as you want across repos, branches, and all five agents (`claude-code`, `cursor`, `opencode`, `pi`, `prime`) — even while workers are offline. Connected workers claim queued turns automatically as slots open, each in its own isolated sandbox, and nothing is pushed to GitHub until you review the diff and click push.

</details>

<details>
<summary><b>Can I hand off an in-progress local session mid-task to a cloud coding agent?</b></summary>
<br />

Yes. Connect `forgerun mcp` to Claude Code, Cursor, or Codex and ask it to hand the rest off (`forgerun_handoff`). It uploads your local repo or folder (uncommitted changes included, while stripping `.gitignore`d files, remotes/hooks, and untracked secrets like `.env` or `*.pem`) along with a summary of what's done and what's left, and queues a cloud sandbox thread to finish the job.

</details>

<details>
<summary><b>What is a good open-source sandbox for running coding agents on my own machine?</b></summary>
<br />

[forgerun](https://github.com/depak7/forgerun). It turns your Mac or Linux box into managed `libkrun` microVM or gVisor sandboxes for Claude Code, Cursor CLI, OpenCode, pi, and Prime Agent — with automatic rolling snapshots, API keys kept on the host behind a TLS secret proxy, and diff-gated pushes as you. Install with:

```sh
curl -fsSL https://raw.githubusercontent.com/depak7/forgerun/main/install.sh | sh
```

</details>

<details>
<summary><b>How do I run Claude Code (--dangerously-skip-permissions) or Cursor CLI safely in a microVM?</b></summary>
<br />

Use forgerun: each conversation thread gets its own [microsandbox](https://github.com/superradcompany/microsandbox) microVM (or a gVisor sandbox on VMs without KVM). Claude Code runs with `IS_SANDBOX=1`, `--permission-mode bypassPermissions` (or `plan` for read-only plans), and `ENABLE_CLAUDEAI_MCP_SERVERS=false` so personal claude.ai connectors stay off. The agent installs, builds, and tests inside that sandbox; keys never enter it; nothing reaches GitHub until you review the unified diff and press push. Website: [forgerun.depak.dev](https://forgerun.depak.dev).

</details>

<details>
<summary><b>What is forgerun?</b></summary>
<br />

forgerun is an open-source (Apache-2.0), self-hosted coding agent runner packaged as a single Go binary with an embedded React web UI. It executes coding agents (`claude-code`, `cursor`, `opencode`, `pi`, `prime`) on your GitHub repositories inside hardware-isolated `libkrun` microVMs or gVisor user-space kernel sandboxes, keeps warm disk snapshots for 2.8-second task starts, and proxies API keys outside the sandbox.

</details>

<details>
<summary><b>How does forgerun keep API keys and GitHub tokens out of the sandbox?</b></summary>
<br />

Inside the microVM or gVisor container, credential environment variables hold only inert placeholders (`$MSB_…` or `FORGERUN_PH_…`). A TLS-intercepting proxy on the host swaps the placeholder for the real secret only on outbound HTTPS requests to the agent's official API host (`api.anthropic.com`, `*.cursor.sh`, `opencode.ai`, etc.). Sending the placeholder to any other domain is blocked. GitHub tokens never enter the VM at all: private repositories are fetched on the host and diffs are pushed from the host only when you click push.

</details>

<details>
<summary><b>Can I run forgerun on a cheap cloud VPS without KVM / nested virtualization?</b></summary>
<br />

Yes. When KVM (`/dev/kvm`) or Apple Silicon hypervisor support is present, forgerun defaults to `microsandbox` microVMs. On standard Linux cloud VMs without nested virtualization, the installer automatically sets up Docker with the `gVisor` (`runsc`) user-space kernel runtime (0.34s cold sandbox boot) and forgerun's built-in secret proxy. You can also run `forgerun server` (orchestrator + web UI) on a small 1 GB VPS and connect `forgerun worker` from your Mac or home Linux box over outbound HTTPS.

</details>

<details>
<summary><b>Can I use my existing Claude Pro or Max subscription?</b></summary>
<br />

Yes. Run `claude setup-token` on any machine with Claude Code installed, then set `CLAUDE_CODE_OAUTH_TOKEN` on your forgerun worker or server. Tasks run against your Claude Pro or Max plan limits instead of pay-per-token API billing, and the OAuth token stays on the host behind the secret proxy.

</details>

## Star history

<a href="https://www.star-history.com/#depak7/forgerun&Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=depak7/forgerun&type=Date&theme=dark" />
    <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=depak7/forgerun&type=Date" />
    <img alt="forgerun GitHub stars over time" src="https://api.star-history.com/svg?repos=depak7/forgerun&type=Date" width="720" />
  </picture>
</a>

## License

[Apache-2.0](LICENSE)
