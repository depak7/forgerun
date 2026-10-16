// Package worker runs queued tasks: claim → run in a sandbox → record outcome.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// Executor runs one task. *task.Runner satisfies it.
type Executor interface {
	Run(ctx context.Context, t task.Task) (task.Result, error)
}

var _ Executor = (*task.Runner)(nil)

var errCancelledByUser = errors.New("cancelled by user")

// Queue is where a pool gets turns and reports how they ended: the
// database for an all-in-one server (store.Store is one), or a remote
// orchestrator for a worker on another machine.
type Queue interface {
	// Claim takes the next queued turn and marks it running; ok is false
	// when there's none.
	Claim(ctx context.Context) (rec store.Record, ok bool, err error)
	Finish(ctx context.Context, id string, o store.Outcome) error
	ThreadTasks(ctx context.Context, threadID string) ([]store.Record, error)
	Diff(ctx context.Context, id string) (string, error)
}

var _ Queue = (store.Store)(nil)

// Pool is a fixed number of workers pulling tasks from a queue.
type Pool struct {
	store   Queue
	exec    Executor
	workers int
	poll    time.Duration
	log     *slog.Logger

	nudge chan struct{}

	// Fetchers, if set, says how a task's repo is fetched: a Fetcher for
	// repos the VM can't clone itself (private ones), nil for the others.
	// owner is the task's user (0 = none), whose GitHub access is used.
	Fetchers func(ctx context.Context, owner int64, repo string) (task.Fetcher, error)

	mu      sync.Mutex
	running map[string]context.CancelCauseFunc // task ID → cancel, for tasks running here
}

func New(s Queue, e Executor, workers int, log *slog.Logger) *Pool {
	return &Pool{
		store:   s,
		exec:    e,
		workers: workers,
		poll:    2 * time.Second,
		log:     log,
		nudge:   make(chan struct{}, 1),
		running: map[string]context.CancelCauseFunc{},
	}
}

// Run starts the workers and blocks until ctx is cancelled and every worker
// has recorded the outcome of its current task.
func (p *Pool) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := range p.workers {
		wg.Go(func() { p.loop(ctx, i) })
	}
	wg.Wait()
}

// Nudge wakes an idle worker. It never blocks.
func (p *Pool) Nudge() {
	select {
	case p.nudge <- struct{}{}:
	default:
	}
}

// Cancel stops a task running in this pool. It reports false if the task
// isn't running here.
func (p *Pool) Cancel(id string) bool {
	p.mu.Lock()
	cancel, ok := p.running[id]
	p.mu.Unlock()
	if ok {
		cancel(errCancelledByUser)
	}
	return ok
}

func (p *Pool) loop(ctx context.Context, n int) {
	log := p.log.With("worker", n)
	for ctx.Err() == nil {
		rec, ok, err := p.store.Claim(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("claiming task", "err", err)
			}
		} else if ok {
			p.execute(ctx, log, rec)
			continue // there may be more work: claim again immediately
		}
		select { // queue empty (or store error): wait for work
		case <-ctx.Done():
		case <-p.nudge:
		case <-time.After(p.poll):
		}
	}
}

func (p *Pool) execute(ctx context.Context, log *slog.Logger, rec store.Record) {
	taskCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil) // always release the context's resources
	p.setRunning(rec.ID, cancel)

	log.Info("task started", "id", rec.ID, "repo", rec.Repo, "thread", rec.ThreadID)
	t := task.Task{ID: rec.ID, ThreadID: rec.ThreadID, ParentID: rec.ParentID, Repo: rec.Repo, Prompt: rec.Prompt,
		AgentID: rec.AgentID, ModelID: rec.Model.ID, Family: rec.Model.Family, Effort: rec.Model.Effort, Plan: rec.Plan}
	var res task.Result
	var err error
	if p.Fetchers != nil {
		t.Fetch, err = p.Fetchers(taskCtx, rec.Owner, rec.Repo)
	}
	if err == nil && t.ParentID != "" {
		err = p.threadContext(taskCtx, rec, &t)
	}
	if err == nil {
		res, err = p.exec.Run(taskCtx, t)
	}
	// Untrack before saving: once the store says the task is finished,
	// Cancel must already report false.
	p.setRunning(rec.ID, nil)
	o := outcome(taskCtx, res, err)

	// taskCtx may be cancelled already; recording the outcome must still happen.
	saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelSave()
	if err := p.store.Finish(saveCtx, rec.ID, o); err != nil {
		log.Error("recording outcome", "id", rec.ID, "err", err)
		return
	}
	log.Info("task finished", "id", rec.ID, "state", o.State, "err", o.Error)
}

// threadContext gives a follow-up what its session needs if it has to be
// rebuilt: the commit the thread started from, and its earlier turns,
// loaded only when asked for.
func (p *Pool) threadContext(ctx context.Context, rec store.Record, t *task.Task) error {
	thread, err := p.store.ThreadTasks(ctx, rec.ThreadID)
	if err != nil {
		return err
	}
	if len(thread) == 0 || thread[0].BaseCommit == "" {
		// The first turn never got as far as a checkout: start afresh.
		return nil
	}
	t.ThreadBase = thread[0].BaseCommit
	t.History = func(ctx context.Context) ([]task.Turn, error) {
		var turns []task.Turn
		for _, prev := range thread {
			if prev.ID == rec.ID {
				break
			}
			turn := task.Turn{Prompt: prev.Prompt, Reply: prev.Reply, Failed: prev.State != task.Succeeded, Plan: prev.Plan}
			if prev.HasDiff && !turn.Failed { // a failed turn's changes were discarded
				if turn.Diff, err = p.store.Diff(ctx, prev.ID); err != nil {
					return nil, err
				}
			}
			turns = append(turns, turn)
		}
		return turns, nil
	}
	return nil
}

// setRunning tracks (cancel != nil) or untracks (nil) a task running here.
func (p *Pool) setRunning(id string, cancel context.CancelCauseFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cancel == nil {
		delete(p.running, id)
	} else {
		p.running[id] = cancel
	}
}

// outcome decides how a finished run is recorded.
func outcome(ctx context.Context, res task.Result, err error) store.Outcome {
	o := store.Outcome{State: task.Succeeded, LogPath: res.LogPath, Diff: res.Diff,
		BaseCommit: res.BaseCommit, Reply: res.Reply, Asked: res.Asked}
	switch {
	case err == nil:
	case errors.Is(context.Cause(ctx), errCancelledByUser):
		o.State, o.Error = task.Cancelled, "cancelled by user"
	case ctx.Err() != nil: // the pool's ctx was cancelled: server shutting down
		o.State, o.Error = task.Failed, "server shut down"
	default: // includes the Runner's own timeout, which only cancels its child context
		o.State, o.Error = task.Failed, err.Error()
	}
	return o
}
