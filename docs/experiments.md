# Experiments and what we learned

forgerun's design rests on a few behaviours of microsandbox and the agent CLIs
that aren't fully documented. Each was checked with a throwaway experiment
before any code was written. This file records what was tested, what happened,
and the decision it led to, so nobody has to rediscover it.

Environment for all experiments: macOS (Apple Silicon), microsandbox CLI/SDK
v0.7.7, guest image `depak7/forgerun-agent-cursor:dev` (Debian, Node 22, git,
Cursor CLI `2026.10.01`). Only dummy secrets were used, except where noted.

---

## 1. Secrets never enter the VM

**Question.** Can an agent use an API key without the key ever being inside the VM?

**Test.** `msb run --secret 'FAKE_KEY@httpbin.org'` with a dummy value, then from
inside the VM: print the variable, call the allowed host with it in a header via
`curl` and `node fetch`, and call a host that isn't allowed.

**Result.**
- Inside the VM the variable holds only a placeholder: `$MSB_FAKE_KEY`.
- Requests to the allowed host arrive with the real value. The microsandbox proxy
  intercepts TLS and swaps the placeholder per request. The guest already trusts the
  proxy's certificate, for both `curl` and Node.
- Sending the placeholder to any other host is blocked (connection reset); the
  default policy is block-and-log.

**Decision.** Agent keys are passed with `WithSecrets` (`sandbox.Secret`), never as
environment variables. The network stays open (the agent needs the web), because
secrets are protected by the proxy regardless.

## 2. Cursor needs HTTP/1.1 behind the proxy

**Question.** Does the Cursor CLI work when its key is proxy-swapped?

**Test.** Run `agent -p "reply with the word hi"` in a VM with `CURSOR_API_KEY`
as a secret for `*.cursor.sh, *.cursor.com`.

**Result.**
- First attempt failed: `expected h2 session, but ALPN protocol failed to negotiate`.
  The intercepting proxy only speaks HTTP/1.1, and Cursor's agent stream required HTTP/2.
- The CLI has a setting for exactly this case (intercepting corporate proxies):
  `~/.cursor/cli-config.json` → `{"network":{"useHttp1ForAgent":true}}`. With it,
  the run succeeded.
- Wildcards like `*.cursor.sh` match multi-level hosts (`agentn.global.api5.cursor.sh`).
- After a run, the CLI's config holds only identity info (`email`, `displayName`,
  `userId`, `authId`), so no access token is stored in the VM.

**Decision.** `Cursor.Run` writes that config file before starting the agent.
Any future agent must be checked the same way, because it has to work over
HTTP/1.1 through the proxy.

## 3. Cursor's model list

**Question.** Which models can Cursor run, and how are options like effort expressed?

**Test.** `agent models` (with a real key, in a VM).

**Result.**
- The default model is `auto` (Cursor picks per request).
- Options are encoded in model IDs, not separate flags:
  `<family>[-thinking][-<effort>][-fast]`, e.g. `claude-opus-5-5-high-fast`.
  Naming is inconsistent: `gpt-5.5-extra-high` vs `xhigh`, and `thinking` sometimes
  comes after the effort (`claude-4.6-opus-high-thinking`).
- Each family's default effort is the variant whose display name has no effort word
  (`claude-opus-5-5-medium` → "Claude Opus 5.5 1M").
- Some models are marked "NO ZDR" (no zero data retention).

**Decision.** The list was parsed **once** into the `models` table (migration
`0003`, 255 models + `auto`), with effort, thinking, fast, default and ZDR stored as
columns, so nothing parses IDs at runtime. Adding a model is an `INSERT`. The API
takes structured choices (`model`, `effort`, `thinking`, `fast`) and resolves them
to an exact ID.

## 4. Snapshots for fast task start

**Question.** Can a repo plus its installed dependencies be prepared once and reused,
so tasks don't clone and install every time? And what happens to secrets?

**Test.** With the `msb` CLI and `expressjs/express`:
1. Cold start in a fresh VM: boot, `git clone`, `npm install`, each timed.
2. Builder VM (with a dummy secret): clone and install, stop, then
   `msb snapshot create` (a disk snapshot, without `--full`).
3. `msb restore` two VMs from it; check dependencies, isolation, `git fetch`, and secrets.
4. Search all of microsandbox's data on disk for the dummy secret value.
5. Attach a different secret to a restored VM with `msb modify … --secret … --restart`.

**Result.**

| | Cold start | From snapshot |
|---|---|---|
| Get a VM | 0.4s boot | 0.4s restore |
| Repo | 6.6s clone | 1.7s `git fetch` |
| Dependencies | 19.9s `npm install` | 0s (304 packages present) |
| Attach task secret | — | 0.7s (modify + restart) |
| **Total** | **26.8s** | **≈ 2.8s** |

- Taking a disk snapshot: 0.3s, 174 MB actual disk (listed as 4 GiB, the virtual size).
- Two VMs restored from one snapshot are isolated (copy-on-write): a file written in
  one does not appear in the other.
- **Secrets are never carried into a restore**, not even with
  `--dangerously-inherit-resources`, and the real secret value was not found anywhere
  on disk. The snapshot doesn't record that a secret existed.
- A secret can be added to a restored VM, but only applied with a restart
  (`--restart`) or on next start. The restart keeps the restored disk. The proxy swap
  then works as usual, and the placeholder stays blocked for other hosts.
- Go SDK equivalents: `RestoreSandbox`, and `Sandbox.Modify` with
  `ModifyOptions.Secrets` (`SecretModifySpec.Value`).

**Gotchas.**
- `msb restore --disk-only` is for cold-booting the disk of a **full** snapshot. On a
  disk snapshot it fails with "disk_only requires a full snapshot with checkpoint state".
  Restore disk snapshots without the flag.
- Removing and re-adding the same secret in one `msb modify` call reports
  "no changes were applied". Do it as two calls.
- A secret added to a running VM without `--restart` has no effect.
- httpbin.org was returning 503 during the test; postman-echo.com was used to verify
  the swap instead. A failed check isn't always our bug.

**Confirmed later from Go** (`internal/sandbox/snapshot_test.go`, real VMs, ~3s per cycle):
- A **running** VM can be snapshotted with `Snapshot.Create` and `GuestFlush: required`,
  and the snapshot includes files written just before. This matters because forgerun
  VMs are ephemeral: stopping one first would delete its disk.
- After `Sandbox.Modify` with the restart policy, the same Go `*Sandbox` handle keeps
  working; no re-attach is needed.
- `SnapshotArtifact.SizeBytes()` returns the **virtual** disk size (4 GiB), not the
  space used, so it isn't useful for disk accounting.

**Decision.** Use disk snapshots, built **without** any agent key. Each task restores
from the repo's snapshot, attaches **its own** secret, fetches the latest code, and
runs. Snapshots therefore contain code and dependencies but never credentials, which
also suits per-user keys later. (Design and implementation: not done yet.)

## 5. Rolling snapshots: deleting, chaining and compaction

**Question.** forgerun's plan is a *rolling* snapshot: after each successful task,
snapshot that task's VM as the repo's new snapshot and delete the old one. Is that
safe? Can the old snapshot be deleted while it's in use, and do snapshots of
restored VMs depend on the snapshot they came from?

**Test.** With the `msb` CLI: snapshot A, restore → write → snapshot B (repeated to
C, D), restore a VM from A and keep it running, then delete A. Then restore from B,
restart the running VM, count layers per generation, and try `msb modify --compact`.

**Result.**
- A snapshot taken from a restored VM records the source as its **parent**.
  microsandbox **refuses** to delete a snapshot that has children
  ("has 1 indexed child snapshot(s); pass --force") unless forced.
- After forcing the delete, the child still restores **with all its data**: each
  snapshot is self-contained, and the parent link is history plus a safety check.
- A VM restored from a snapshot **keeps working** after that snapshot is deleted:
  it can read, write, sync and even restart.
- **Each generation adds a disk layer**: B had 2 layers, C 3, D 4. Rolling once per
  task would grow the stack without limit.
- `msb modify NAME --compact` (SDK: `Sandbox.Compact`) merged a restored VM's
  5 layers into 2 in 1.7s, pausing it only 21 ms. The snapshot taken afterwards had
  2 layers and all data. Its real size grew (41 MB vs 6 MB) because the merge writes
  the combined data into one layer.
- Real sizes were small here (4–6 MB uncompacted) because only files written on top
  of the image are stored. Empty snapshot group folders (lock files, 4 KB) remain
  after all members are deleted.

**Decision.**
- Deleting the replaced snapshot needs **force**, which is safe because children are
  self-contained and running VMs don't need their source.
- **Compact the task VM before snapshotting it** (or once its layer count passes a
  threshold), so the rolling chain stays short.

## 6. Snapshot groups have a head

**Found while building step 3** (not a planned experiment): the first version kept
every repo's snapshots in one group, `forgerun`. Replacing a repo's snapshot then failed:
`cannot remove current head snap_…; first select another snapshot with 'msb snapshot
head forgerun:<snapshot>'`.

**What microsandbox does.**
- A group is meant to be one **lineage**, and the SDK defaults the group to the source
  sandbox's name. `group.json` records a **head**.
- The first snapshot in a group becomes its head. A snapshot of a **fresh** VM (no parent)
  doesn't move the head; a snapshot of a VM restored from the head presumably does, which
  is why §5's chains never hit this.
- The head **can't be deleted while the group has other members**, not even with
  `--force`. Deleting a group's **last** member is fine, even if it's the head.
- `Snapshot.GroupHead(ctx, "group:member")` (CLI: `msb snapshot head group:member`)
  selects a new head.
- Deleting the group's last member leaves the group folder behind with just `group.json`.
- Removing a snapshot whose folder is already gone fails with a plain
  `io error: No such file or directory`, not `ErrSnapshotNotFound`. `DeleteSnapshot`
  treats a missing folder as already deleted.

**Don't touch the files directly.** Removing a snapshot folder with `rm` leaves
microsandbox's index (`~/.microsandbox/db/msb.db`, table `snapshot_index`) and the group's
head pointing at nothing. After that, every new snapshot in the group fails its integrity
check ("group head … is missing"), and `msb snapshot rm` and `reindex` can't fix it. The
fix was deleting the index row in `msb.db` and moving the group folder aside. Always use
`msb snapshot rm`.

**Decision.**
- **One group per (repo, agent)**, named `forgerun-<agent>-<repo-name>-<hash>`, so the
  group's head *is* forgerun's current snapshot.
- `TakeSnapshot` selects each new snapshot as its group's head, so the snapshot it
  replaces can be deleted.
- Evicting a repo entirely (step 5) deletes its group's only member, which is always
  allowed. Empty group folders are left for the step 5 sweep.

## 7. Opening a PR from the host with a fine-grained PAT

**Question.** Can forgerun's server (not the VM) turn a task's changes into a branch,
a commit and a pull request? Which way: plain `git` on the host, or GitHub's API only?

**Setup.** A throwaway repo (`depak7/playground`), a fine-grained PAT limited to it with
*Contents: read/write* and *Pull requests: read/write*. A test patch made exactly as forgerun
makes one (`git diff --cached --binary`), covering an edit, a new file, a delete, a
rename with an edit, a mode change (+x) and a binary change.

**Result.**

| | A1: `git` on the host | A2: GitHub API only |
|---|---|---|
| Steps | shallow fetch of base → `git apply --index` → commit → push → open PR (API) | one blob per changed file → tree (`base_tree`) → commit → ref → open PR |
| Time | **5.2s** (fetch 1.3, apply 0.2, commit+push 2.2, PR 1.5) | **8.8s** (5 blobs: 7.2s, PR 1.6) |
| Grows with | repo size a little (shallow fetch) | number of changed files (one call each) |
| Input | the patch we already save | the changed files' contents (binary patches use git's own format, which only git can apply) |
| Host needs | `git` | nothing |
| All six change types, +x mode, binary byte-for-byte | ✅ | ✅ |
| Works for GitLab etc. | push yes; only "open PR" differs | no, GitHub-specific |

- **Authentication for host `git`** without the token in the URL or command line:
  `GIT_CONFIG_COUNT/KEY/VALUE` env vars setting
  `http.https://github.com/.extraheader = Authorization: Basic base64("x-access-token:" + token)`
  and an empty `credential.helper` (so the macOS keychain isn't consulted).
- **A follow-up commit** pushed to the same branch updated the open PR (1 → 2 commits).
- **Commits are "unsigned" either way** with a PAT. (GitHub only marks API-made commits
  as Verified for GitHub Apps.) Author/committer names are whatever we set.
- **Errors are readable**, good enough to store on a task as-is:
  - token lacks a permission → HTTP 403 "Resource not accessible by personal access token",
    with header `x-accepted-github-permissions: contents=write` naming what's missing;
  - PR already open for the branch → 422 "A pull request already exists for …";
  - creating a branch that exists → 422 "Reference already exists";
  - pushing to a branch whose history moved (e.g. someone added a commit to the PR) →
    `! [rejected] … (non-fast-forward)`; without force, git never overwrites others' commits.
- A brand-new empty repo has no `main` yet; the PAT could create the first commit through
  the contents API once *Contents: write* was granted.
- zsh gotchas while scripting: a variable named `path` replaces `PATH`; quote URLs
  containing `?`.

**Decision.** Use **A1 (host `git`)**: faster, works from the patch forgerun already
saves, handles every change type through git itself, and only the "open PR" call is
host-specific, so other git hosts fit later. The token stays on the host; the VM never
sees it. Follow-ups fetch the PR branch and push on top (never force).

## 8. A conversation summary by continuing Cursor's chat

**Question.** After a task, can forgerun get a summary of the *conversation*, covering
what the user asked and shared as well as what the agent did, by continuing the agent's
own chat? Does a rolling summary carry a thread forward in a new chat? What does a
diff-only summary lose?

**Test** (`out/summary-exp.sh`, real key as a proxy secret, a tiny Node repo):
1. A task whose prompt carries constraints: add `greet(name)`, keep README unchanged,
   no new dependencies, add and run a test. `--output-format json`.
2. In the same VM: `agent -p "<summary request>" --continue`.
3. A **new** chat given summary 1 + a follow-up (add `farewell`), then a summary of the
   whole thread.
4. A fresh chat asked only to summarize `git diff`.

**Result.**
- `--output-format json` returns `type, subtype, is_error, duration_ms, duration_api_ms,
  result, session_id, request_id, usage`. **`session_id` is the chat's ID** (it's the
  folder name under `~/.cursor/chats/<workspace-hash>/`), so `--resume <session_id>`
  can target that exact chat.
- **Continuing the chat gave the summary we want** (24s): "Requested" listed every
  constraint the user gave; "Done" listed the changes, why, and the test run with its
  result; "Open" named a real gap. It changed no files.
- **The rolling summary works** (task 68s, summary 37s): the new chat, seeded only with
  summary 1, produced a summary covering both requests and both pieces of work, with
  the constraints carried over.
- **Diff-only loses the user's side** (37s): it described the code but dropped the
  constraints, and turned a test that had been run into an unchecked "test plan" item.
- Agent CLI flags: `--resume [chatId]`, `--continue`, `create-chat`, `ls`, `resume`;
  `--output-format text|json|stream-json`. `--model` also accepts bracket options, e.g.
  `'claude-opus-4-8[context=1m,effort=high,fast=false]'`.
- Storage: `~/.cursor/chats/…` plus `~/.cursor/projects/<workspace>/agent-transcripts`,
  2.3 MB after four runs. These are wiped before snapshotting, so the summary must be
  taken before cleanup.
- **Network:** the first attempt printed `Connection lost, reconnecting to
  https://api2.cursor.sh (attempt 1)` and was stopped by hand. The rerun printed the same
  line but recovered on its own. Host checks: HTTPS to api2.cursor.sh fine both directly
  (~0.8s) and through the VM proxy (~1.0s); the host network had 5% packet loss. In JSON
  mode the CLI prints nothing until it finishes, so a slow run and a stuck one look the
  same; forgerun should detect long silence itself.

**Decision.**
- PR tasks end with a summary step: `--resume <session_id>` (from the task's JSON
  output) with a fixed summary request, before cleanup. Agents that can't continue a
  chat fall back to a diff-only summary.
- The summary is cumulative: a follow-up's prompt carries the previous summary, and its
  own summary covers the whole thread. The latest summary is the PR description.

---

## 9. Claude Code in a forgerun VM with a setup token

**Question.** Can Claude Code run unattended in our VM, authenticated by a subscription
setup token that never enters the VM? Does it support the end-of-task conversation
summary like Cursor? How do model, effort and output behave?

**Test** (`out/claude-exp.sh`): the CLI installed with npm in the Cursor image (Node 22),
`CLAUDE_CODE_OAUTH_TOKEN` as a microsandbox secret for `api.anthropic.com` only. Dry runs
with a dummy token first, then the owner's real token.

**Result.**
- **Auth through the proxy works** with the setup token, first try, with no
  `~/.claude.json` onboarding tweak needed. The VM only sees the placeholder
  (`$MSB_CLAUDE_CODE_OAUTH_TOKEN`); the CLI doesn't validate the token's format locally.
  No HTTP/2 problem (unlike Cursor). A dummy token gives `401 OAuth access token is invalid`.
- **Running as root:** `--dangerously-skip-permissions` / `--permission-mode
  bypassPermissions` are refused as root ("cannot be used with root/sudo privileges")
  unless `IS_SANDBOX=1` is set. With it, `--permission-mode bypassPermissions` works.
- **stdin:** every `claude -p` waits 3s for piped input unless stdin is closed
  (`< /dev/null`); empty stdin removes the wait.
- **Task:** the constrained `greet` task was done correctly in 6s (default model
  `claude-sonnet-5-5`). JSON output includes `session_id`, `is_error`, `modelUsage`,
  `total_cost_usd`, `num_turns`, `result` and more.
- **Summary:** `claude -p "<summary request>" --continue --permission-mode plan` (6s) gave
  a summary listing every constraint the user gave, what was done, how it was verified
  (`node test.js` printed ok), and real open points. It changed no files. `--continue`
  means "the most recent conversation in the current directory".
- **Model and effort:** `--model` takes aliases (`fable`, `opus`, `sonnet`) or full names;
  `--effort low|medium|high|xhigh|max` is a separate flag. `--model sonnet --effort low`
  ran on `claude-sonnet-5-5`.
- **Text mode doesn't stream:** a prompt that printed five numbers with `sleep 2` between
  them produced only the final message, after 18s. For the watchdog, activity comes from
  file changes, including the session transcript under `~/.claude/projects/`.
- **State:** `~/.claude.json` (the CLI's own config, 51 KB after a few runs) and
  `~/.claude/` (688 KB): `projects/<folder>/<session>.jsonl` transcripts, a per-project
  `memory/` folder (auto-memory), `session-env/`, `backups/`, `remote-settings.json`.
- `--bare` must not be used: it never reads OAuth tokens.
- A setup token can be revoked from the Claude UI (owner's note); the docs only state it
  lasts one year.

**Decision.**
- Adapter: image with the CLI preinstalled; run `claude -p <prompt> --permission-mode
  bypassPermissions --output-format text` with `IS_SANDBOX=1`, `HOME=/root`, stdin closed;
  `--model <alias>` and `--effort <level>` from the chosen model.
- Summary: `--continue --permission-mode plan`, stdin closed.
- StatePaths: `/root/.claude` and `/root/.claude.json` (wiped before snapshots, so one
  task's auto-memory and transcripts never reach the next).
- Secret: `CLAUDE_CODE_OAUTH_TOKEN` for `api.anthropic.com` only.
- Because effort is a separate flag, the agent request carries the model's family and
  effort, not just its ID.

## 10. Rolling snapshots grow even when nothing changes

**Question.** The playground/cursor snapshot grew on every task (20.6 → 31.4 → 73 → 84 MB),
even for tasks that changed nothing. What is growing?

**Test** (a throwaway program in `out/snapexp`): restore the current snapshot into a test VM,
measure what's in it, try to give freed space back, snapshot it into a test group, compare.

**Results.**
- Inside the VM, `df /` reported **7 MB** used; the snapshot's layer files took **71 MB**.
- `/` is an overlay; its writable layer is an ext4 disk (`/dev/vdb`) the runtime mounts out of
  the agent's view. `fstrim` on the overlay fails ("discard not supported"); mounting `/dev/vdb`
  and trimming it reports 3.9 GiB trimmed, yet the new snapshot was **still 71.8 MB**.
- So the qcow2 layers keep every block ever written: each task's temporary files (git objects,
  agent state, caches), deleted by the cleanup, stay in the snapshot. Compaction merges layers
  but doesn't drop freed blocks. The SDK (v0.7.7) has no discard or sparsify option.

**Decision.** forgerun works around it: after saving, it compares the snapshot's size with
the VM's used bytes and drops the snapshot when it's over 2× plus 64 MB; the next task starts
cold and saves a compact one (real run: 84.5 MB dropped → cold start → 16.8 MB). The proper
fix is in microsandbox (pass discards through to the layer files); worth reporting upstream.

---

## 11. Read-only turns: plans and questions

**Question.** Can each agent run a turn that reads and proposes without editing, and does the
answer reach forgerun's text output?

**Results.**
- Claude Code: `--permission-mode plan` prints its plan as text in `-p` mode.
- Cursor CLI: `--mode plan` ("read-only/planning") ran, but printed **nothing** in headless
  text mode; its plan never reached the output. `--mode ask` (read-only Q&A) prints the full
  answer, and with forgerun's plan instruction it writes a proper plan (what changes, why,
  how it'll be checked).
- Asking instead of guessing: with a short note after every message ("if something important
  is unclear, don't guess; reply under a `## Questions` heading"), Cursor answered the vague
  prompt "add a palindrome function in it" with three questions (file, language, rules)
  instead of picking `script.sh` and a lenient rule as it had before.

**Decision.** `agent.Request.ReadOnly`: Claude Code uses plan mode, Cursor uses ask mode, and
forgerun discards any changes a read-only turn makes anyway. A reply with a `## Questions`
heading and no changes marks the turn as waiting for the user.

---

## Researched, not yet tested: Claude Code authentication

Based on community guides and issue reports (not official docs), to be confirmed by
an experiment before writing an adapter:

- Headless options: `ANTHROPIC_API_KEY`; a subscription token from `claude setup-token`
  passed as `CLAUDE_CODE_OAUTH_TOKEN`; or Bedrock/Vertex.
- Chosen for forgerun: **subscription token only**. It's a request header, so the proxy
  swap should work for `api.anthropic.com`.
- Reported issues: first-run onboarding can ignore the token unless
  `hasCompletedOnboarding` is set in `~/.claude.json`; some reports of 401 with
  `claude -p`; the token doesn't refresh when it expires.
- Bedrock/Vertex were ruled out: AWS credentials sign requests inside the VM, so the
  proxy can't swap them and the real secret would have to enter the VM.
