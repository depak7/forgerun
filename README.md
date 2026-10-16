# forgerun

Run coding agents in the cloud — each in its own microVM — and talk to them
like a teammate: give a task, read the diff, ask for changes, and push a
branch or open a pull request only when you're happy.

forgerun is a single Go binary with a web UI. It runs [Cursor CLI](https://cursor.com/cli)
and [Claude Code](https://www.anthropic.com/claude-code) today; agents are
pluggable.

- **Isolated:** every thread gets its own [microsandbox](https://github.com/superradcompany/microsandbox)
  microVM. The agent can install, build and test whatever it needs.
- **Secrets never enter the VM:** the VM only sees placeholders; a proxy on the
  host swaps in the real key, and only for the agent's own API hosts.
- **A live session per thread:** the VM stays up between your messages, so the
  agent keeps its own conversation and working tree.
- **Review, then push:** each turn's changes are kept as a diff. Nothing reaches
  GitHub until you press push — then it's one commit on a readable
  `forgerun/…` branch, with a pull request if you want one.
- **Warm starts:** when a session closes, its VM is saved as the repo's
  snapshot; the next thread on that repo starts from it in seconds.

> Status: early. It runs on one machine, has no user accounts yet, and listens
> on `127.0.0.1` by default.

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

## Quick start

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

An agent without its key is still listed; only its tasks are refused.

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
| `-timeout` | `15m` | max time per turn |
| `-agent-idle` | `10m` | stop an agent that prints nothing and changes nothing |
| `-snapshot-budget` | `20GB` | disk for warm-start snapshots |
| `-require-zdr` | off | offer only zero-data-retention models |

Run `./forgerun serve -h` for all of them.

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
| `GET /api/threads`, `/api/agents`, `/api/models`, `/api/status`, `/api/snapshots` | lists for the UI |

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
cmd/forgerun/       serve (API + workers + UI) and run (one task, no database)
internal/agent/     agent adapters: Cursor, Claude Code
internal/task/      the runner: sessions, turns, replay
internal/sandbox/   the only package that talks to microsandbox
internal/snapshots/ warm-start snapshots and their cleanup
internal/publish/   pushing branches and pull requests (host-side git)
internal/store/     Postgres (migrations) and an in-memory store for tests
internal/worker/    the worker pool
internal/api/       HTTP handlers
web/                the React UI (embedded with -tags webui)
images/             agent VM images
docs/experiments.md what each experiment showed, and why things are the way they are
```

## License

[Apache-2.0](LICENSE)
