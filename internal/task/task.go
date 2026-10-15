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
	"slices"
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
	AgentID string // which agent runs it; "" = Runner.DefaultAgent
	ModelID string // exact agent model id; "" or "auto" lets the agent choose
	Family  string // the model's family, e.g. "opus"; "" if unknown
	Effort  string // the model's effort, e.g. "high"; "" = the agent's default
	PR      bool   // also open a pull request; the changes are pushed to a branch either way
	// ParentID, for a follow-up, is the task it continues; "" otherwise.
	ParentID string
	// FollowUp is set by the worker when the task continues a thread's
	// branch; nil for a fresh task.
	FollowUp *FollowUp
}

// FollowUp is what a follow-up task needs to know about its thread.
type FollowUp struct {
	Branch     string   // the thread's branch, e.g. "forgerun/add-a-test-9fe9cc"
	PRNumber   int      // the thread's pull request to update; 0 = none yet
	ThreadBase string   // the commit the thread's first task started from
	Summary    string   // the latest summary of the thread; "" if none
	Requests   []string // earlier requests in the thread, oldest first
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
	Dir          string // out/<task-id>
	LogPath      string
	Diff         string // the changes (git diff --binary); "" if the agent changed nothing
	BaseCommit   string // the commit the agent started from
	Summary      string // the agent's conversation summary; "" if none
	Branch       string // where the changes were pushed; "" if they weren't
	PRURL        string // the pull request, if one was opened or updated
	PRNumber     int
	PublishError string // why the changes weren't pushed, or a wanted pull request wasn't opened
}

// Runner holds settings shared by every task.
type Runner struct {
	// Agents are the agents tasks can run on, with their credentials.
	Agents agent.Set
	// DefaultAgent runs tasks that name no agent (e.g. from the CLI).
	DefaultAgent string
	OutDir       string
	CPUs         uint8
	MemMiB       uint32
	Timeout      time.Duration
	Stdout       io.Writer // live progress; also saved to the log file
	// AgentIdle stops an agent that produces no output and changes no
	// files for this long, failing the task early. 0 = never.
	AgentIdle time.Duration
	// Snapshots, if set, saves the VM after each successful task so later
	// tasks for the same repo can start from it. nil = no snapshots.
	Snapshots Snapshotter
	// Publisher, if set, pushes every task's changes to a branch and opens
	// pull requests for tasks that ask for one. nil = changes stay in
	// Result.Diff only (the CLI).
	Publisher Publisher
}

func (r *Runner) Run(ctx context.Context, t Task) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	// Absolute, so the saved paths (also stored in the database) work from
	// any directory: the publisher runs git elsewhere, for one.
	outDir, err := filepath.Abs(r.OutDir)
	if err != nil {
		return Result{}, err
	}
	res := Result{Dir: filepath.Join(outDir, t.ID)}
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

	ag, err := r.Agents.Get(cmp.Or(t.AgentID, r.DefaultAgent))
	if err != nil {
		return res, err
	}
	// The credential is fetched now, for this task only.
	secrets, err := r.Agents.Secrets(ctx, ag)
	if err != nil {
		return res, err
	}
	cfg := sandbox.Config{
		Image:       ag.Image(),
		CPUs:        r.CPUs,
		MemoryMiB:   r.MemMiB,
		MaxLifetime: r.Timeout + snapshotTimeout + 2*time.Minute, // backstop if we crash
		Secrets:     secrets,
	}
	start := time.Now()
	sb := r.restore(ctx, ag, t, cfg, step)
	warm := sb != nil
	if !warm {
		step("creating sandbox %s (%s)", t.ID, ag.Image())
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

	if t.FollowUp != nil {
		step("checking out %s", t.FollowUp.Branch)
		if err := checkoutThread(ctx, sb, t.FollowUp); err != nil {
			return res, err
		}
	}
	if res.BaseCommit, err = sb.Run(ctx, sandbox.Cmd{Name: "git", Args: []string{"-C", repoDir, "rev-parse", "HEAD"}}); err != nil {
		return res, fmt.Errorf("reading start commit: %w", err)
	}
	res.BaseCommit = strings.TrimSpace(res.BaseCommit)

	step("running %s agent (model: %s)", ag.ID(), cmp.Or(t.ModelID, "auto"))
	if err := r.runAgent(ctx, ag, sb, t, logs, step); err != nil {
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
		res.Diff = diff
		files, _ := git("diff", "--cached", "--name-only")
		step("changed %d file(s)", len(strings.Fields(files)))
		// A summary for the pull request and as context for follow-ups;
		// asked before the snapshot cleanup wipes the agent's chat.
		r.summarize(ctx, ag, sb, t, &res, step)
	}
	r.saveSnapshot(ctx, ag, sb, t, step)
	r.publish(ctx, t, &res, step)
	return res, nil
}

// runAgent runs the agent, under a watchdog when AgentIdle is set.
func (r *Runner) runAgent(ctx context.Context, ag agent.Agent, sb *sandbox.Sandbox, t Task, logs io.Writer, step func(string, ...any)) error {
	req := agent.Request{Dir: repoDir, Prompt: prompt(t), ModelID: t.ModelID, Family: t.Family, Effort: t.Effort}
	if r.AgentIdle <= 0 {
		return ag.Run(ctx, sb, req, logs)
	}
	agentCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	dog := newWatchdog(r.AgentIdle, fileProbe(sb, append([]string{repoDir}, ag.StatePaths()...)))
	go dog.Watch(agentCtx, stop)

	err := ag.Run(agentCtx, sb, req, dog.Writer(logs))
	if cause := context.Cause(agentCtx); errors.Is(cause, errStalled) {
		step("stopped the agent: %v", cause)
		return cause
	}
	return err
}

// fileProbe reports whether any file under paths changed since the last
// call, by comparing modification times against a marker file in the VM.
// Errors count as "no change"; the watchdog keeps going.
func fileProbe(sb *sandbox.Sandbox, paths []string) func(context.Context) bool {
	const marker = "/tmp/.forgerun-activity"
	// -newer compares against the marker; -print -quit stops at the first hit.
	script := `find "$@" -newer ` + marker + ` -print -quit 2>/dev/null; touch ` + marker
	sb.Run(context.Background(), sandbox.Cmd{Name: "touch", Args: []string{marker}})
	return func(ctx context.Context) bool {
		out, err := sb.Run(ctx, sandbox.Cmd{Name: "sh", Args: append([]string{"-c", script, "probe"}, paths...)})
		return err == nil && strings.TrimSpace(out) != ""
	}
}

// summarize asks the agent, in its own conversation, for a summary of what
// was asked and done. Agents that can't continue a conversation give none;
// the pull request then describes the request and the changed files.
func (r *Runner) summarize(ctx context.Context, ag agent.Agent, sb *sandbox.Sandbox, t Task, res *Result, step func(string, ...any)) {
	c, ok := ag.(agent.Continuer)
	if !ok {
		step("%s agent can't continue its conversation; no summary", ag.ID())
		return
	}
	step("asking the agent for a summary")
	ask := SummaryPrompt
	if t.FollowUp != nil {
		ask += "\n\n" + followUpSummaryNote
	}
	summary, err := c.Continue(ctx, sb, repoDir, ask)
	if err != nil || summary == "" {
		step("warning: no summary: %v", cmp.Or(err, errors.New("empty reply")))
		return
	}
	res.Summary = summary
	step("got a summary (%d lines)", strings.Count(summary, "\n")+1)
}

// publishTimeout bounds pushing and opening a pull request after the task.
const publishTimeout = 2 * time.Minute

// publish pushes the changes to the thread's branch and opens or updates
// its pull request. The task has already succeeded, so a failure only goes
// into res.PublishError; the changes are still in res.Diff.
func (r *Runner) publish(ctx context.Context, t Task, res *Result, step func(string, ...any)) {
	switch {
	case res.Diff == "":
		if t.PR {
			res.PublishError = "the agent made no changes, so there is nothing to push"
		}
		return
	case r.Publisher == nil:
		res.PublishError = "publishing is off: the changes were not pushed"
	case ctx.Err() != nil:
		res.PublishError = "the task was cancelled before publishing"
	}
	if res.PublishError != "" {
		step("not pushed: %s", res.PublishError)
		return
	}
	step("pushing the changes")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	req := PublishRequest{
		TaskID: t.ID, Repo: t.Repo, BaseCommit: res.BaseCommit, Patch: res.Diff,
		Prompt: t.Prompt, Summary: res.Summary, Requests: []string{t.Prompt}, OpenPR: t.PR,
	}
	if fu := t.FollowUp; fu != nil {
		req.Branch, req.PRNumber = fu.Branch, fu.PRNumber
		req.Requests = append(slices.Clone(fu.Requests), t.Prompt)
	}
	pub, err := r.Publisher.Publish(ctx, req)
	res.Branch, res.PRURL, res.PRNumber = pub.Branch, pub.PRURL, pub.PRNumber // set even on error: the push may have worked
	if res.Branch != "" {
		step("pushed to branch %s", res.Branch)
	}
	if err != nil {
		res.PublishError = err.Error()
		step("warning: %v", err)
		return
	}
	switch {
	case res.PRNumber == 0:
	case req.PRNumber > 0:
		step("pull request updated: %s", res.PRURL)
	default:
		step("pull request opened: %s", res.PRURL)
	}
}

// restore returns a VM started from the repo's snapshot, ready to run the
// agent, or nil if the task must start cold: snapshots are off, the repo
// has none yet, or it couldn't be used. A snapshot never fails a task.
func (r *Runner) restore(ctx context.Context, ag agent.Agent, t Task, cfg sandbox.Config, step func(string, ...any)) *sandbox.Sandbox {
	if r.Snapshots == nil {
		return nil
	}
	sb, err := r.Snapshots.Restore(ctx, RestoreRequest{
		TaskID: t.ID, Repo: t.Repo, AgentID: ag.ID(), RepoDir: repoDir, Config: cfg,
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
func (r *Runner) saveSnapshot(ctx context.Context, ag agent.Agent, sb *sandbox.Sandbox, t Task, step func(string, ...any)) {
	if r.Snapshots == nil || ctx.Err() != nil { // off, or the task was cancelled
		return
	}
	// The task's context may be about to time out; snapshotting gets its
	// own time limit instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), snapshotTimeout)
	defer cancel()
	err := r.Snapshots.Save(ctx, sb, SnapshotRequest{
		TaskID: t.ID, Repo: t.Repo, AgentID: ag.ID(), RepoDir: repoDir, StatePaths: ag.StatePaths(),
	}, step)
	if err != nil {
		step("warning: snapshot not saved: %v", err)
	}
}
