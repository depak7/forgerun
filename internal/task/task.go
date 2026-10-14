// Package task runs one coding task end to end in a fresh sandbox.
package task

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/sandbox"
)

const repoDir = "/work/repo"

// snapshotTimeout bounds saving a snapshot after the task. It's separate
// from Runner.Timeout so a task that finishes near its deadline can still
// be snapshotted.
const snapshotTimeout = 2 * time.Minute

// Task is what the user asked for.
type Task struct {
	ID      string
	Repo    string // git URL
	Prompt  string
	ModelID string // exact agent model id; "" or "auto" lets the agent choose
	PR      bool   // open a pull request with the changes
}

// New creates a task with a unique, sortable ID like "t-20261011-142233-a1b2c3".
func New(repo, prompt string) Task {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	id := fmt.Sprintf("t-%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b))
	return Task{ID: id, Repo: repo, Prompt: prompt}
}

// Result is what a task produced. Dir and LogPath are set even on failure,
// so you can always read the log.
type Result struct {
	Dir         string // out/<task-id>
	LogPath     string
	PatchPath   string // "" if the agent changed nothing
	BaseCommit  string // the commit the agent started from
	Summary     string // the agent's conversation summary (PR tasks); "" if none
	SummaryPath string
	PRURL       string // the pull request, if one was opened
	PRError     string // why a requested pull request wasn't opened
}

// Runner holds settings shared by every task.
type Runner struct {
	Agent   agent.Agent
	OutDir  string
	CPUs    uint8
	MemMiB  uint32
	Timeout time.Duration
	Stdout  io.Writer // live progress; also saved to the log file
	// Snapshots, if set, saves the VM after each successful task so later
	// tasks for the same repo can start from it. nil = no snapshots.
	Snapshots Snapshotter
	// Publisher, if set, opens pull requests for tasks that ask for one.
	// nil = pull requests are off.
	Publisher Publisher
}

func (r *Runner) Run(ctx context.Context, t Task) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	res := Result{Dir: filepath.Join(r.OutDir, t.ID)}
	if err := os.MkdirAll(res.Dir, 0o755); err != nil {
		return res, err
	}
	res.LogPath = filepath.Join(res.Dir, "agent.log")
	logFile, err := os.Create(res.LogPath)
	if err != nil {
		return res, err
	}
	defer logFile.Close()
	logs := io.MultiWriter(r.Stdout, logFile)

	step := func(format string, a ...any) { fmt.Fprintf(logs, "==> "+format+"\n", a...) }

	cfg := sandbox.Config{
		Image:       r.Agent.Image(),
		CPUs:        r.CPUs,
		MemoryMiB:   r.MemMiB,
		MaxLifetime: r.Timeout + snapshotTimeout + 2*time.Minute, // backstop if we crash
		Secrets:     r.Agent.Secrets(),
	}
	start := time.Now()
	sb := r.restore(ctx, t, cfg, step)
	warm := sb != nil
	if !warm {
		step("creating sandbox %s (%s)", t.ID, r.Agent.Image())
		if sb, err = sandbox.New(ctx, t.ID, cfg); err != nil {
			return res, err
		}
	}
	defer func() {
		step("destroying sandbox")
		if err := sb.Destroy(); err != nil {
			fmt.Fprintf(logs, "warning: destroy %s: %v\n", sb.Name(), err)
		}
	}()
	if !warm {
		step("cloning %s", t.Repo)
		if _, err := sb.Run(ctx, sandbox.Cmd{Name: "git", Args: []string{"clone", "--depth=1", t.Repo, repoDir}}); err != nil {
			return res, fmt.Errorf("clone: %w", err)
		}
	}
	mode := "cold"
	if warm {
		mode = "warm"
	}
	step("sandbox ready in %s (%s)", time.Since(start).Round(100*time.Millisecond), mode)

	if res.BaseCommit, err = sb.Run(ctx, sandbox.Cmd{Name: "git", Args: []string{"-C", repoDir, "rev-parse", "HEAD"}}); err != nil {
		return res, fmt.Errorf("reading start commit: %w", err)
	}
	res.BaseCommit = strings.TrimSpace(res.BaseCommit)

	step("running %s agent (model: %s)", r.Agent.ID(), cmp.Or(t.ModelID, "auto"))
	if err := r.Agent.Run(ctx, sb, agent.Request{Dir: repoDir, Prompt: t.Prompt, ModelID: t.ModelID}, logs); err != nil {
		return res, fmt.Errorf("agent: %w", err)
	}

	step("collecting changes")
	git := func(args ...string) (string, error) {
		return sb.Run(ctx, sandbox.Cmd{Name: "git", Args: append([]string{"-C", repoDir}, args...)})
	}
	if _, err := git("add", "-A"); err != nil {
		return res, fmt.Errorf("git add: %w", err)
	}
	diff, err := git("diff", "--cached", "--binary")
	if err != nil {
		return res, fmt.Errorf("git diff: %w", err)
	}
	if diff == "" {
		step("agent made no changes")
	} else {
		res.PatchPath = filepath.Join(res.Dir, "changes.patch")
		if err := os.WriteFile(res.PatchPath, []byte(diff), 0o644); err != nil {
			return res, fmt.Errorf("saving patch: %w", err)
		}
		step("patch saved to %s", res.PatchPath)
	}

	if t.PR && res.PatchPath != "" {
		r.summarize(ctx, sb, &res, step) // before the snapshot cleanup wipes the chat
	}
	r.saveSnapshot(ctx, sb, t, step)
	if t.PR {
		r.publish(ctx, t, &res, step)
	}
	return res, nil
}

// summarize asks the agent, in its own conversation, for a summary of what
// was asked and done. Agents that can't continue a conversation give none;
// the pull request then describes the request and the changed files.
func (r *Runner) summarize(ctx context.Context, sb *sandbox.Sandbox, res *Result, step func(string, ...any)) {
	c, ok := r.Agent.(agent.Continuer)
	if !ok {
		step("%s agent can't continue its conversation; no summary", r.Agent.ID())
		return
	}
	step("asking the agent for a summary")
	summary, err := c.Continue(ctx, sb, repoDir, SummaryPrompt)
	if err != nil || summary == "" {
		step("warning: no summary: %v", cmp.Or(err, errors.New("empty reply")))
		return
	}
	res.Summary = summary
	res.SummaryPath = filepath.Join(res.Dir, "summary.md")
	if err := os.WriteFile(res.SummaryPath, []byte(summary+"\n"), 0o644); err != nil {
		step("warning: saving summary: %v", err)
		res.SummaryPath = ""
	}
}

// publishTimeout bounds opening a pull request after the task.
const publishTimeout = 2 * time.Minute

// publish opens the pull request. The task has already succeeded, so a
// failure only goes into res.PRError; the patch is still there.
func (r *Runner) publish(ctx context.Context, t Task, res *Result, step func(string, ...any)) {
	switch {
	case res.PatchPath == "":
		res.PRError = "the agent made no changes, so there is nothing to publish"
	case r.Publisher == nil:
		res.PRError = "pull requests are disabled on this server"
	case ctx.Err() != nil:
		res.PRError = "the task was cancelled before publishing"
	}
	if res.PRError != "" {
		step("no pull request: %s", res.PRError)
		return
	}
	step("opening a pull request")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	url, err := r.Publisher.Publish(ctx, PublishRequest{
		TaskID: t.ID, Repo: t.Repo, BaseCommit: res.BaseCommit, PatchPath: res.PatchPath,
		Prompt: t.Prompt, Summary: res.Summary,
	})
	if err != nil {
		res.PRError = err.Error()
		step("warning: pull request not opened: %v", err)
		return
	}
	res.PRURL = url
	step("pull request opened: %s", url)
}

// restore returns a VM started from the repo's snapshot, ready to run the
// agent, or nil if the task must start cold: snapshots are off, the repo
// has none yet, or it couldn't be used. A snapshot never fails a task.
func (r *Runner) restore(ctx context.Context, t Task, cfg sandbox.Config, step func(string, ...any)) *sandbox.Sandbox {
	if r.Snapshots == nil {
		return nil
	}
	sb, err := r.Snapshots.Restore(ctx, RestoreRequest{
		TaskID: t.ID, Repo: t.Repo, AgentID: r.Agent.ID(), RepoDir: repoDir, Config: cfg,
	}, step)
	switch {
	case errors.Is(err, ErrNoSnapshot):
		step("no snapshot of this repo yet; starting cold")
		return nil
	case err != nil:
		step("warning: snapshot not usable, starting cold: %v", err)
		return nil
	}
	return sb
}

// saveSnapshot keeps the VM as the repo's snapshot. The task has already
// succeeded, so a failure here is only a warning in the log.
func (r *Runner) saveSnapshot(ctx context.Context, sb *sandbox.Sandbox, t Task, step func(string, ...any)) {
	if r.Snapshots == nil || ctx.Err() != nil { // off, or the task was cancelled
		return
	}
	// The task's context may be about to time out; snapshotting gets its
	// own time limit instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), snapshotTimeout)
	defer cancel()
	err := r.Snapshots.Save(ctx, sb, SnapshotRequest{
		TaskID: t.ID, Repo: t.Repo, AgentID: r.Agent.ID(), RepoDir: repoDir, StatePaths: r.Agent.StatePaths(),
	}, step)
	if err != nil {
		step("warning: snapshot not saved: %v", err)
	}
}
