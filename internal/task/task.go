// Package task runs coding turns: each message in a thread is a turn, run
// by an agent in the thread's session, a VM kept between turns.
package task

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/sandbox"
)

const repoDir = "/work/repo"

// snapshotTimeout bounds saving a snapshot when a session closes.
const snapshotTimeout = 2 * time.Minute

// sessionLifetime is the VM-side kill switch for a session's VM: the
// runtime stops it after this long even if forgerun crashed and never
// closed it. Sessions normally close long before, when idle.
const sessionLifetime = 12 * time.Hour

// Task is one turn: a message to the agent in a thread.
type Task struct {
	ID       string
	ThreadID string // the thread's first task; "" = this task starts a thread
	ParentID string // the task this one follows; "" for a thread's first task
	Repo     string // git URL
	Prompt   string
	AgentID  string // which agent runs it; "" = Runner.DefaultAgent
	ModelID  string // exact agent model id; "" or "auto" lets the agent choose
	Family   string // the model's family, e.g. "opus"; "" if unknown
	Effort   string // the model's effort, e.g. "high"; "" = the agent's default
	// Plan makes this a plan-only turn: the agent reads and proposes, and
	// any changes it makes are discarded.
	Plan bool
	// ThreadBase is the commit the thread's first turn started from; ""
	// for a first turn (it starts from the default branch's latest).
	ThreadBase string
	// History loads the thread's earlier turns, oldest first. It's called
	// only when the thread's session has to be (re)built; nil = none.
	History func(ctx context.Context) ([]Turn, error)
}

// Turn is an earlier turn of a thread, enough to rebuild its session.
type Turn struct {
	Prompt string
	Reply  string // what the agent answered; "" if nothing
	Diff   string // the changes it made (git diff --binary); "" if none
	Failed bool
	Plan   bool // a plan-only turn: its reply is a plan
}

func (t Task) thread() string { return cmp.Or(t.ThreadID, t.ID) }

// New creates a task with a unique, sortable ID like "t-20261011-142233-a1b2c3".
func New(repo, prompt string) Task {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	id := fmt.Sprintf("t-%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b))
	return Task{ID: id, Repo: repo, Prompt: prompt}
}

// Result is what a turn produced. Dir and LogPath are set even on failure,
// so you can always read the log.
type Result struct {
	Dir        string // out/<task-id>
	LogPath    string
	Diff       string // this turn's changes (git diff --binary); "" if none
	Reply      string // the end of what the agent wrote; "" if nothing
	BaseCommit string // the commit the session's work started from
	Asked      bool   // the agent asked the user questions instead of going on
}

// Runner runs turns and keeps each thread's session between them.
type Runner struct {
	// Agents are the agents tasks can run on, with their credentials.
	Agents agent.Set
	// DefaultAgent runs tasks that name no agent (e.g. from the CLI).
	DefaultAgent string
	OutDir       string
	CPUs         uint8
	MemMiB       uint32
	Timeout      time.Duration // per turn
	Stdout       io.Writer     // live progress; also saved to each turn's log
	Log          *slog.Logger  // session events; nil = discard
	// AgentIdle stops an agent that produces no output and changes no
	// files for this long, failing the turn early. 0 = never.
	AgentIdle time.Duration
	// Snapshots, if set, saves a session's VM when it closes so later
	// sessions for the same repo can start from it. nil = no snapshots.
	Snapshots Snapshotter
	// SessionIdle closes a session unused for this long; 0 = never.
	SessionIdle time.Duration
	// MaxSessions is how many sessions may be open at once; the least
	// recently used idle one is closed to make room. 0 = no limit.
	MaxSessions int

	sessions *sessions
}

func (r *Runner) open() *sessions {
	if r.sessions == nil { // set up on first use; the Runner is a plain struct
		r.sessions = newSessions(r.SessionIdle, r.MaxSessions)
	}
	return r.sessions
}

func (r *Runner) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// Run runs one turn in its thread's session, opening the session if it
// isn't open. The session stays open afterwards, even if the turn fails.
func (r *Runner) Run(ctx context.Context, t Task) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	// Absolute, so the saved paths (also stored in the database) work from
	// any directory.
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
	logs := io.MultiWriter(cmp.Or[io.Writer](r.Stdout, io.Discard), logFile)
	step := func(format string, a ...any) { fmt.Fprintf(logs, "==> "+format+"\n", a...) }

	ag, err := r.Agents.Get(cmp.Or(t.AgentID, r.DefaultAgent))
	if err != nil {
		return res, err
	}
	ss, history, err := r.acquire(ctx, ag, t, step)
	if err != nil {
		return res, err
	}
	defer r.open().release(ss)
	sb := ss.sb
	git := func(ctx context.Context, args ...string) (string, error) {
		return sb.Run(ctx, sandbox.Cmd{Name: "git", Args: append([]string{"-C", repoDir}, args...)})
	}
	if res.BaseCommit, err = git(ctx, "rev-parse", "HEAD"); err != nil {
		r.broken(ss, err)
		return res, fmt.Errorf("reading the session's commit: %w", err)
	}
	res.BaseCommit = strings.TrimSpace(res.BaseCommit)

	_, continuer := ag.(agent.Continuer)
	req := agent.Request{Dir: repoDir, Prompt: withGuidance(t.Prompt, t.Plan), ModelID: t.ModelID, Family: t.Family, Effort: t.Effort, ReadOnly: t.Plan}
	switch {
	case ss.continued && continuer:
		req.Continue = true
		step("running %s agent (model: %s), continuing its conversation", ag.ID(), cmp.Or(t.ModelID, "auto"))
	case len(history) > 0:
		req.Prompt = replayPrompt(history, req.Prompt)
		step("running %s agent (model: %s) with the thread so far", ag.ID(), cmp.Or(t.ModelID, "auto"))
	default:
		step("running %s agent (model: %s)", ag.ID(), cmp.Or(t.ModelID, "auto"))
	}
	reply := &tailBuffer{max: maxReply}
	if err := r.runAgent(ctx, ag, sb, req, io.MultiWriter(logs, reply), step); err != nil {
		res.Reply = reply.String()
		// Keep the session in step with the database: a failed turn
		// records no changes, so it keeps none.
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer done()
		if _, rerr := git(cleanup, "reset", "-q", "--hard"); rerr != nil {
			r.broken(ss, rerr)
		} else if _, cerr := git(cleanup, "clean", "-fdq"); cerr != nil {
			r.broken(ss, cerr)
		}
		step("the turn's changes were discarded")
		return res, fmt.Errorf("agent: %w", err)
	}
	res.Reply = reply.String()
	ss.continued = continuer
	if t.Plan {
		// A plan changes nothing, whatever the agent did.
		if _, err := git(ctx, "reset", "-q", "--hard"); err != nil {
			r.broken(ss, err)
			return res, fmt.Errorf("discarding a plan's changes: %w", err)
		}
		if _, err := git(ctx, "clean", "-fdq"); err != nil {
			r.broken(ss, err)
			return res, fmt.Errorf("discarding a plan's changes: %w", err)
		}
		res.Asked = asksQuestions(res.Reply)
		step("plan ready; waiting for the user")
		return res, nil
	}

	step("collecting changes")
	if _, err := git(ctx, "add", "-A"); err != nil {
		return res, fmt.Errorf("git add: %w", err)
	}
	diff, err := git(ctx, "diff", "--cached", "--binary")
	if err != nil {
		return res, fmt.Errorf("git diff: %w", err)
	}
	if diff == "" {
		if res.Asked = asksQuestions(res.Reply); res.Asked {
			step("the agent asked questions; waiting for the user's answer")
		} else {
			step("agent made no changes")
		}
		return res, nil
	}
	res.Diff = diff
	files, _ := git(ctx, "diff", "--cached", "--name-only")
	// Committed in the VM, so the next turn's diff holds only its own changes.
	if _, err := git(ctx, "-c", "user.name=forgerun", "-c", "user.email=forgerun@localhost", "commit", "-q", "--no-verify", "-m", "turn "+t.ID); err != nil {
		return res, fmt.Errorf("git commit: %w", err)
	}
	step("changed %d file(s); not pushed yet", len(strings.Fields(files)))
	return res, nil
}

// acquire returns the thread's session, busy, opening one if needed. It
// also returns the thread's history when the session was just built from
// it, so the agent gets the conversation as text.
func (r *Runner) acquire(ctx context.Context, ag agent.Agent, t Task, step func(string, ...any)) (*session, []Turn, error) {
	ss, err := r.open().take(ctx, t.thread())
	if err != nil {
		return nil, nil, err
	}
	if ss != nil {
		switch {
		case ss.agentID != ag.ID():
			step("the thread switched from %s to %s: closing its %s session", ss.agentID, ag.ID(), ss.agentID)
			r.open().drop(ss)
			r.closeSession(ss, "agent changed")
		case !alive(ctx, ss.sb):
			step("the session's VM is gone; reopening it")
			r.open().drop(ss)
			r.closeSession(ss, "VM gone")
		default:
			step("continuing session %s", ss.sb.Name())
			return ss, nil, nil
		}
	}

	ss, evicted := r.open().reserve(t.thread(), ag.ID())
	ss.repo = t.Repo
	for _, e := range evicted {
		step("closing the least recently used session to make room")
		r.closeSession(e, "too many sessions")
	}
	history, err := r.openSession(ctx, ag, t, ss, step)
	if err != nil {
		r.open().drop(ss)
		if ss.sb != nil {
			r.destroy(ss.sb)
		}
		return nil, nil, err
	}
	r.log().Info("session opened", "thread", ss.thread, "agent", ss.agentID, "vm", ss.sb.Name())
	return ss, history, nil
}

// openSession boots the session's VM, from the repo's snapshot when there
// is one, and rebuilds the thread's work in it: the thread's start commit
// plus every earlier turn's changes, each as a commit.
func (r *Runner) openSession(ctx context.Context, ag agent.Agent, t Task, ss *session, step func(string, ...any)) ([]Turn, error) {
	// The credential is fetched now, for this session only.
	secrets, err := r.Agents.Secrets(ctx, ag)
	if err != nil {
		return nil, err
	}
	cfg := sandbox.Config{Image: ag.Image(), CPUs: r.CPUs, MemoryMiB: r.MemMiB, MaxLifetime: sessionLifetime, Secrets: secrets}
	start := time.Now()
	ss.sb = r.restore(ctx, ag, t, cfg, step)
	warm := ss.sb != nil
	if !warm {
		step("creating sandbox %s (%s)", t.ID, ag.Image())
		if ss.sb, err = sandbox.New(ctx, t.ID, cfg); err != nil {
			return nil, err
		}
		step("cloning %s", t.Repo)
		if _, err := ss.sb.Run(ctx, sandbox.Cmd{Name: "git", Args: []string{"clone", "--depth=1", t.Repo, repoDir}}); err != nil {
			return nil, fmt.Errorf("clone: %w", err)
		}
	}
	var history []Turn
	if t.History != nil {
		if history, err = t.History(ctx); err != nil {
			return nil, fmt.Errorf("loading the thread: %w", err)
		}
	}
	if t.ThreadBase != "" {
		step("rebuilding the thread's work (%d earlier turn(s))", len(history))
		if err := rebuild(ctx, ss.sb, t.ThreadBase, history); err != nil {
			return nil, err
		}
	}
	mode := "cold"
	if warm {
		mode = "warm"
	}
	step("session ready in %s (%s)", time.Since(start).Round(100*time.Millisecond), mode)
	return history, nil
}

// CloseSession closes a thread's idle session, saving its VM as the
// repo's snapshot. It returns ErrNoSession or ErrSessionBusy if it can't.
func (r *Runner) CloseSession(thread string) error {
	ss, err := r.open().remove(thread)
	if err != nil {
		return err
	}
	r.closeSession(ss, "ended by the user")
	return nil
}

// Session reports a thread's session.
func (r *Runner) Session(thread string) SessionInfo { return r.open().info(thread) }

// OpenSessions is how many sessions are open.
func (r *Runner) OpenSessions() int { return r.open().count() }

// RunSessions closes idle sessions until ctx ends. Close the rest with
// CloseAll once no more turns will run.
func (r *Runner) RunSessions(ctx context.Context) {
	every := time.Minute
	if r.SessionIdle > 0 {
		every = min(every, max(r.SessionIdle/4, time.Second))
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, ss := range r.open().expired() {
				r.closeSession(ss, "idle")
			}
		}
	}
}

// CloseAll closes every idle session, saving their VMs as snapshots.
func (r *Runner) CloseAll() {
	for _, ss := range r.open().removeAll() {
		r.closeSession(ss, "server stopping")
	}
}

// Summarize asks the agent in a thread's open session for a summary of the
// conversation, read-only. It returns "" if the agent can't give one, and
// ErrNoSession if the thread's session is closed.
func (r *Runner) Summarize(ctx context.Context, thread string) (string, error) {
	ss, err := r.open().take(ctx, thread)
	if err != nil {
		return "", err
	}
	if ss == nil {
		return "", ErrNoSession
	}
	defer r.open().release(ss)
	ag, err := r.Agents.Get(ss.agentID)
	if err != nil {
		return "", err
	}
	c, ok := ag.(agent.Continuer)
	if !ok || !ss.continued {
		return "", nil
	}
	return c.Continue(ctx, ss.sb, repoDir, SummaryPrompt)
}

// closeSession saves the session's VM as the repo's snapshot (the work in
// progress is in the database, so nothing is lost) and destroys it.
func (r *Runner) closeSession(ss *session, reason string) {
	log := r.log().With("thread", ss.thread, "agent", ss.agentID, "reason", reason)
	if ss.sb == nil {
		return
	}
	if r.Snapshots != nil {
		ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
		ag, err := r.Agents.Get(ss.agentID)
		if err == nil {
			err = r.Snapshots.Save(ctx, ss.sb, SnapshotRequest{
				TaskID: ss.sb.Name(), Repo: ss.repo, AgentID: ss.agentID, RepoDir: repoDir, StatePaths: ag.StatePaths(),
			}, func(format string, a ...any) { log.Info(fmt.Sprintf(format, a...)) })
		}
		cancel()
		if err != nil {
			log.Warn("session snapshot not saved", "err", err)
		}
	}
	r.destroy(ss.sb)
	log.Info("session closed", "vm", ss.sb.Name())
}

func (r *Runner) destroy(sb *sandbox.Sandbox) {
	if err := sb.Destroy(); err != nil {
		r.log().Warn("destroying VM", "vm", sb.Name(), "err", err)
	}
}

// broken drops a session whose VM stopped answering: the next turn
// rebuilds it from the database.
func (r *Runner) broken(ss *session, err error) {
	r.log().Warn("session broken; it will be rebuilt", "thread", ss.thread, "err", err)
	r.open().drop(ss)
	go r.destroy(ss.sb)
}

// alive reports whether a session's VM still runs commands.
func alive(ctx context.Context, sb *sandbox.Sandbox) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := sb.Run(ctx, sandbox.Cmd{Name: "true"})
	return err == nil
}

// runAgent runs the agent, under a watchdog when AgentIdle is set.
func (r *Runner) runAgent(ctx context.Context, ag agent.Agent, sb *sandbox.Sandbox, req agent.Request, logs io.Writer, step func(string, ...any)) error {
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

// restore returns a VM started from the repo's snapshot, or nil if the
// session must start cold: snapshots are off, the repo has none yet, or it
// couldn't be used. A snapshot never fails a turn.
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
