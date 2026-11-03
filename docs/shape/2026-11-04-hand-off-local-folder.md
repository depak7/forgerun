# Hand off a local folder to forgerun

Created: 2026-11-04
Category: Integration
Status: Final
Research: Quick

## Problem Statement

Today forgerun only works on GitHub repos: a task takes an `https://` git URL, and the sandbox clones it. Work in progress on your own machine can't move to forgerun. That covers a folder that isn't on GitHub, uncommitted changes, a single file, and a conversation you're in the middle of with a local coding agent.

The owner wants to stop partway through a local agent session and say "do the rest in the cloud". The local agent would write a summary of the conversation, send a copy of the folder it's working in, and create a forgerun task that a worker carries on. The result comes back as a diff, or as a branch or PR when the folder is a GitHub repo.

This builds on what forgerun already does well. Coding agents run in sandboxes on your own workers, keys stay out of the VM, and threads use summaries that survive a change of agent. What's new is the way in: from a local folder, through the local agent.

## Core User Flows

### Flow 1: Set up once
1. In forgerun's Settings, the user creates a personal token and copies it. (On a server without sign-in, no token is needed.)
2. The user adds forgerun's MCP server to their local agent's MCP settings, with the server's address and the token. The MCP server is a command of the forgerun binary that runs on their machine.

### Flow 2: Hand off mid-conversation
1. The user is working with a local agent in a folder, and says "hand the rest of this off to forgerun".
2. The agent writes a handoff summary: what the user asked for, what has been done and decided, and what's left.
3. The agent calls the handoff tool with that summary, the folder (or one file), and optionally the agent and model forgerun should use.
4. The MCP server copies the folder. It leaves out whatever `.gitignore` ignores, and never sends `.env*` and similar secret files, even when there's no `.gitignore`. It uploads the copy to the forgerun server and creates the task.
5. The agent tells the user the thread was created and gives a link to it in the web UI.
6. A worker gets the copy into a sandbox and runs the chosen agent, with the summary as its prompt. The user's local folder is untouched.

### Flow 3: Check on it and get the result
1. Later, in the same conversation or a new one, the user asks the local agent how the handoff is going.
2. The agent calls a status tool. It reports whether the task is running, finished, failed, or waiting on the user (questions or a plan), and the agent's reply.
3. When the task has finished, the agent fetches the diff through a tool and shows it. The user, or the agent, applies it with `git apply`.
4. The thread is also visible in the web UI like any other: log, diff, follow-ups.

### Flow 4: Push or open a PR
1. The folder is a git repo whose remote is on GitHub, and the commit it was copied at already exists on GitHub.
2. From the web UI, or with the push option on the handoff, forgerun pushes the thread's work as a branch on top of that commit, and can open a PR as today.
3. The branch contains the user's uncommitted changes from when they handed off, plus the agent's work. That's intended: it's the whole job.
4. If the commit isn't on GitHub (unpushed local commits), or the folder isn't a GitHub repo, only the diff is available, and forgerun says why.

## Scope

### In Scope
- **A local folder or a single file as a task's source**, as an alternative to a GitHub URL:
  - copied in, never mounted live
  - `.gitignore` respected, and secret files (`.env*` and the like) always left out
  - git history included when the folder is a git repo
- **A local MCP server in the forgerun binary**, with tools to:
  - hand off: folder, summary, and optionally agent and model
  - check status and the agent's reply
  - fetch the diff
- **Sending the copy from the orchestrator to whichever worker runs the turn**, local or remote.
- **Personal tokens:** create, list and revoke in Settings. A token acts as its owner: their threads, their GitHub token for pushes and PRs.
- **Pushing or opening a PR** for a folder task, when the folder is a GitHub repo and its base commit is on GitHub.
- **Follow-ups** on a folder thread (from the web UI) continue in the same sandbox session, as they do today.
- **Warm snapshots** for folder tasks: a later task on the same folder starts from a snapshot rather than cold.
- **A limit on upload size**, with a clear error when a folder is too big.
- **Docs:** setting up the MCP server and a token, and what is and isn't copied.

### Explicitly Out of Scope
- **Live mount or two-way sync.** Workers are on other machines, and a live mount would let the agent change your files immediately. Local changes made after the handoff don't reach the task; send a follow-up instead.
- **Starting a folder task from the web UI.** The MCP tool is the way in. The web UI shows and continues these threads, but has no browser folder upload.
- **A tool that writes the result back into your folder.** The diff is fetched, and you or the local agent run `git apply`.
- **Moving the local agent's chat history itself into the sandbox.** The summary carries the context, so the cloud side can be a different agent or model. Chat history can also contain secrets the agent saw.
- **Agent-specific `/handoff` commands** (e.g. a skill for one agent). The MCP tool works for any agent with MCP; per-agent wrappers can come later.

## Technical Context
- **Relevant architecture:**
  - Tasks are created through `POST /api/tasks`, and follow-ups through `POST /api/tasks/{id}/followups`. Request bodies are capped at 1 MiB.
  - Workers dial out to the orchestrator and long-poll for turns (`internal/remote`). Diffs and logs already travel from worker to orchestrator. Nothing travels the other way yet, apart from the turn itself.
  - A turn clones the repo inside the VM with `git clone --depth=1` (`internal/task/task.go`, `internal/task/fetch.go`), and follow-ups continue the open session.
  - Diffs are computed against the commit the session's work started from (`BaseCommit` / `ThreadBase`).
- **Constraints:**
  - `validateRepo` accepts only `https://` URLs.
  - Snapshots are grouped per (repo URL, agent) (`snapshots.Group`).
  - Publishing pushes with the host's `git`, applying the saved patch on a shallow fetch of the base commit (pr-and-threads design). So the base must exist on the remote.
  - Sign-in is a GitHub OAuth cookie (`internal/api/auth.go`). There are no API tokens today; local mode (no `GITHUB_OAUTH_CLIENT_ID`) has no auth.
  - Secrets reach the VM only through the proxy. A copied `.env` would go around that, which is why secret files are never sent.
  - Any new dependency (e.g. an MCP library) must pass a security check first.
- **Existing code:**
  - `internal/api`: task creation, auth, sessions and push
  - `internal/remote` and `internal/worker`: the worker protocol
  - `internal/task`: the session and how a turn gets its code
  - `internal/snapshots`: the snapshot grouping
  - `internal/store` and `internal/store/postgres`: storage and migrations
  - `cmd/forgerun`: the binary's commands
  - the web UI's Settings page: for tokens

## Key Decisions
| Decision | Choice | Why |
|----------|--------|-----|
| Which feature first | Folder source, with handing off built on top (one feature, since the MCP tool is the way in) | Owner's choice. Handing off doesn't then depend on pushing work to GitHub first |
| How files reach the sandbox | Copy in, diff out; never a live mount | Owner's choice. Works with remote workers, and the local folder stays untouched |
| What's copied | `.gitignore` respected; secret files always left out; git history included for a repo | Owner's choice |
| Way in | A local MCP server, run by the local agent, that summarizes and creates the task | Owner's choice. Only a server on the user's machine can read their folder |
| Context for the cloud agent | The local agent's handoff summary, not its chat history | Matches the decided rolling-summary design. Allows another agent or model and keeps the agent's chat history out of the sandbox |
| Auth for the MCP tool | Personal tokens from Settings; none needed in local mode | Owner's choice. Works with a hosted server |
| Results | Diff always; also branch or PR when the folder is a GitHub repo and its base commit is on GitHub | Owner's choice. Otherwise there's no base on the remote to push onto |
| Is it a git repo, and where to push | The MCP tool asks the local git: the repo's top folder, the branch you're on, the remote it tracks (else `origin`), and that remote's URL. A GitHub URL is the push target; any other host, or no remote, means diff only | Owner agreed. The tool runs in your folder, so local git knows; nothing is guessed |
| Base commit | The commit you're on, with uncommitted changes and untracked (not ignored) files in the copy. The server checks with GitHub that the commit exists in that repo; if not, it says "push your commits first, or take the diff" | Owner agreed. Local git can be out of date |
| What gets pushed | A new branch `forgerun/<name>-<id>` from the base commit: your uncommitted changes, then the agent's work. Never your own branch, never a force-push. A PR targets the branch you were on, else the repo's default branch | Owner agreed |
| A subfolder of a repo | Copied from the repo's top folder, so paths in the diff and the branch match the repo | Owner agreed |
| Warm snapshots | In scope for folder tasks | Owner pulled it back in. How a folder is identified for snapshots is left to implementation planning |
| Follow-ups on a folder thread | From the web UI; no MCP follow-up tool in this feature | My default, not stated by the owner: keeps the MCP tools to hand off, status and diff. Easy to add later |
| Upload size | Capped, with a clear error | My default, not stated by the owner. The exact limit is set during implementation planning |
