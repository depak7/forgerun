# forgerun

[![License: Apache-2.0](https://img.shields.io/github/license/depak7/forgerun)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/depak7/forgerun)](go.mod)
[![CI](https://github.com/depak7/forgerun/actions/workflows/ci.yml/badge.svg)](https://github.com/depak7/forgerun/actions/workflows/ci.yml)

Run Claude Code, Cursor CLI and OpenCode on your repo, each in its own microVM, on your
own box. Review the diff, then push.

<!-- Demo: a 30–60 s clip goes here (new task → the agent works → diff → one follow-up →
push and open a PR), as a gif, with the same clip as an mp4. -->

- **26.8 s → 2.8 s to start a task.** A new thread starts from a disk snapshot of the repo
  with its dependencies installed, instead of cloning and installing again (measured on
  `expressjs/express`: [docs/experiments.md](docs/experiments.md#4-snapshots-for-fast-task-start)).
- **Keys never enter the VM.** The VM sees only placeholders; a proxy on the host swaps in
  the real key, and only for the agent's own API.
- **Workers dial out over HTTPS only.** Run the web UI on a small server and the VMs on
  your own machine: nothing needs opening on it.

## Quick start

You need a Mac with Apple Silicon, or Linux with KVM (`ls /dev/kvm`). Then:

```sh
curl -fsSL https://raw.githubusercontent.com/depak7/forgerun/main/install.sh | sh
```

It asks what the machine is for (a **worker** for an orchestrator elsewhere, or
**everything** on this machine) and for any keys, then does the rest by itself: installs
what's missing (git, a C compiler, Go; Node for the web UI), builds forgerun into
`~/forgerun`, checks the setup with `forgerun doctor`, and runs it in the background
(systemd on Linux, launchd on macOS), now and at every boot. Run it again to update.

`forgerun doctor` checks a machine's setup any time and says how to fix what's wrong:
virtualization (KVM, `kvm` group, a VM without nested virtualization), the microsandbox
runtime and its database, disk and memory, image downloads, the orchestrator and worker
token, Postgres, `GITHUB_TOKEN` and the sign-in settings. It boots one microVM to time a
cold start (`-quick` skips that).

```sh
~/forgerun/forgerun doctor -env ~/.forgerun-worker.env   # a worker's settings
```

To set it up step by step instead, see [By hand](#by-hand).

## How it works

forgerun is a single Go binary with a web UI. Agents are pluggable adapters
([`internal/agent`](internal/agent)).

- **Isolated:** every thread gets its own microVM. The agent can install, build and test
  whatever it needs.
- **A live session per thread:** the VM stays up between your messages, so the agent keeps
  its own conversation and working tree.
- **Review, then push:** each turn's changes are kept as a diff. Nothing reaches GitHub
  until you press push — then it's one commit on a readable `forgerun/…` branch, with a
  pull request if you want one.
- **Warm starts:** when a session closes, its VM is saved as the repo's snapshot; the next
  thread on that repo starts from it in seconds.

**Built on [microsandbox](https://github.com/superradcompany/microsandbox)** (libkrun
microVMs). forgerun isn't a sandbox provider: it's what runs around the agents (sessions,
review, pushing, workers, sign-in).

> Status: early. It works end to end (sign-in with GitHub, per-user limits, workers on
> other machines), and things still change quickly.

## How it compares

| | Self-hosted | Open source | A VM per task | Keys readable inside the sandbox |
|---|---|---|---|---|
| **forgerun** | Yes | Yes (Apache-2.0) | Yes: a microVM per thread | No: placeholders, swapped by a proxy outside the VM |
| Cursor cloud agents | Partly: [self-hosted machines][c2] run the tools; Cursor runs the agent loop | No | Yes on Cursor's cloud ([isolated VMs][c1]); not on self-hosted machines, where [several agents share one][c2] | Yes: [environment-variable secrets are visible][c3] to the agent |
| OpenAI Codex cloud | No | No | Yes: [each cloud task runs in a VM][x1] | Partly: environment variables yes; [network secrets][x1] are placeholders swapped by a proxy |
| OpenHands | Yes | Yes (MIT) | No: [Docker containers][o1] | Yes: [custom secrets are exported][o2] into the agent's environment |
| Claude Code on your own VPS | Yes | No ([Claude Code][a3] is proprietary; its [sandbox runtime][a1] is open) | No by default: an [OS-level sandbox][a1] for shell commands; a VM is [yours to build][a2] | Yes by default; a [mask mode][a1] covers shell commands only |

Checked against each vendor's own docs on 2026-10-24.

[c1]: https://cursor.com/docs/cloud-agent
[c2]: https://cursor.com/docs/cloud-agent/self-hosted
[c3]: https://cursor.com/docs/cloud-agent/security-network
[x1]: https://learn.chatgpt.com/docs/environments/cloud-environments
[o1]: https://docs.openhands.dev/openhands/usage/sandboxes/overview
[o2]: https://docs.openhands.dev/openhands/usage/settings/secrets-settings
[a1]: https://code.claude.com/docs/en/sandboxing
[a2]: https://code.claude.com/docs/en/sandbox-environments
[a3]: https://github.com/anthropics/claude-code/blob/main/LICENSE.md

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
        Proxy["Secrets proxy<br/>(microsandbox)"]
    end

    subgraph VM["microVM per thread"]
        Agent["Agent CLI<br/>Cursor · Claude Code"]
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

An agent without its key is still listed; only its tasks are refused, except on models that need no key.

**Sign-in (optional).** Without these, forgerun runs in local mode: no accounts, everything
is yours. With them, people sign in with GitHub and see only their own threads.

| Variable | What for |
|---|---|
| `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` | a GitHub OAuth App (callback: `<FORGERUN_PUBLIC_URL>/auth/github/callback`) |
| `FORGERUN_PUBLIC_URL` | where browsers reach forgerun, e.g. `https://forgerun.example.com` |
| `FORGERUN_SECRET_KEY` | encrypts users' GitHub tokens at rest: `openssl rand -hex 32` |
| `FORGERUN_ADMINS` | GitHub usernames that see everything and the settings |

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

Run `./forgerun serve -h` for all of them.

## One machine, or orchestrator and workers apart

`forgerun serve` runs everything in one process: the web UI, the API and the VMs.
To run the VMs elsewhere (for example the orchestrator on a small VPS, the VMs on a
machine at home), use two roles of the same binary:

```sh
# the orchestrator: web UI, API, Postgres, sign-in, pushing. No virtualization needed.
FORGERUN_WORKER_TOKEN=… ./forgerun server

# a worker, on any macOS (Apple Silicon) or Linux (KVM) machine. It dials out over
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

To deploy it for other people (orchestrator on a server with HTTPS and sign-in, a worker on
your machine), see [docs/deploying.md](docs/deploying.md): a Dockerfile and a compose file
with automatic HTTPS are included (`deploy/`).

## API

The UI uses the same JSON API (`{"success": true, "data": …}`):

| | |
|---|---|
| `POST /api/tasks` | start a thread: `{repo, prompt, agent?, model?, effort?}` |
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
FORGERUN_TEST_DATABASE_URL='postgres://forgerun:forgerun@localhost:5433/forgerun_test?sslmode=disable' \
  go test ./internal/store/...                   # against Postgres
FORGERUN_INTEGRATION=1 go test ./internal/snapshots/   # boots real microVMs
cd web && npm test && npx tsc --noEmit           # web UI
make dev-web                                     # UI with live reload on :5173
```

```
cmd/forgerun/       serve (everything), server (orchestrator only), worker, doctor, run (one task, no database)
install.sh          the one-step installer (worker or everything on one machine)
internal/doctor/    forgerun doctor's checks
internal/agent/     agent adapters: Cursor, Claude Code
internal/task/      the runner: sessions, turns, replay
internal/sandbox/   the only package that talks to microsandbox
internal/snapshots/ warm-start snapshots and their cleanup
internal/publish/   pushing branches and pull requests (host-side git)
internal/store/     Postgres (migrations) and an in-memory store for tests
internal/worker/    the worker pool
internal/remote/    workers on other machines: the orchestrator side and the worker side
internal/api/       HTTP handlers
web/                the React UI (embedded with -tags webui)
images/             agent VM images
docs/experiments.md what each experiment showed, and why things are the way they are
```

## License

[Apache-2.0](LICENSE)
