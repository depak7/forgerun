# Contributing to forgerun

Thanks for helping. Small, focused pull requests are easiest to review; for anything bigger,
open an issue or a discussion first so we can agree on the shape.

## Getting set up

You need a Mac with Apple Silicon or Linux with KVM (only for the tests that boot real VMs),
Go (see `go.mod`), Node 22, Docker (for Postgres) and git.

```sh
git clone https://github.com/depak7/forgerun && cd forgerun
docker compose up -d                 # Postgres on 127.0.0.1:5433 (dev and test databases)
cp .env.example .env                 # fill in what you need; see the README
make build                           # the web UI and ./forgerun
set -a && source .env && set +a && ./forgerun serve
make dev-web                         # the UI with live reload on :5173, talking to :8080
```

`./forgerun doctor` tells you what's missing on your machine.

## Tests

```sh
go vet ./... && go test ./...                                   # DB and VM tests skip
FORGERUN_TEST_DATABASE_URL='postgres://forgerun:forgerun@localhost:5433/forgerun_test?sslmode=disable' \
  go test ./internal/store/...                                  # against Postgres
FORGERUN_INTEGRATION=1 go test ./internal/snapshots/ ./internal/sandbox/   # boots real microVMs
FORGERUN_GVISOR=1 go test ./internal/sandbox/ -run GVisorReal                # Linux with Docker + runsc
cd web && npx tsc --noEmit && npm test                          # the web UI
```

CI runs all of these except the VM tests. Run those yourself when you touch
`internal/sandbox`, `internal/snapshots` or `internal/task`.

## Where things are

| | |
|---|---|
| `cmd/forgerun/` | the commands: `serve`, `server`, `worker`, `doctor`, `run` |
| `internal/agent/` | agent adapters (Cursor, Claude Code, OpenCode) |
| `internal/task/` | the runner: sessions, turns, replay |
| `internal/sandbox/` | the sandbox backends: microsandbox (microVMs) and gVisor |
| `internal/secretproxy/` | the key-swapping proxy gVisor sandboxes use |
| `internal/api/`, `web/` | the HTTP API and the React UI |
| `internal/remote/` | workers on other machines |
| `internal/doctor/` | `forgerun doctor`'s checks |
| `internal/store/postgres/migrations/` | the database schema; models are rows here |
| `docs/experiments.md` | what each experiment showed, and why things are the way they are |

## Adding an agent

An agent is an adapter: one Go file, an image, and its models as database rows.

1. **Try it by hand first** in a microVM: does its CLI run unattended, where does it keep
   state, how does it continue a conversation, is there a read-only mode? Write what you
   found as a new section in `docs/experiments.md` (see §9 and §12 for examples).
2. **The image:** `images/agent-<name>/Dockerfile`, with the CLI version pinned.
3. **The adapter:** `internal/agent/<name>.go` implementing `agent.Agent` (and
   `Continuer` if it can continue its conversation), with a test like `opencode_test.go`.
   Add it to `Builtin()` in `internal/agent/set.go`.
4. **Its credential:** `Credential()` names the variable, `Secrets()` the hosts the proxy
   may send the key to. The key must never enter the VM.
5. **Its models:** a migration in `internal/store/postgres/migrations/` that inserts the
   agent and its models (see `0013_opencode.sql`). Models that need no key are `keyless`.

## Conventions

- `gofmt`, `go vet` clean, and a test with every change in behaviour.
- Comments say why, in plain sentences. Error messages say what to do.
- Nothing provider-specific in the repo: deploy docs stay generic.
- Secrets never go in logs, commits or the VM.
- Commit messages: a short summary line, then what changed and why.

## Reporting a security problem

Please don't open a public issue. Write to the maintainer through GitHub
(<https://github.com/depak7>) and we'll sort it out privately.
