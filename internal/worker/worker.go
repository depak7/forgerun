// Package worker runs queued tasks: claim → run in a sandbox → record outcome.
package worker

import (
	"context"
	"errors"
	"fmt"
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

// Pool is a fixed number of workers pulling tasks from a store.
type Pool struct {
	store   store.Store
	exec    Executor
	workers int
	poll    time.Duration
	log     *slog.Logger

	nudge chan struct{}

	mu      sync.Mutex
	running map[string]context.CancelCauseFunc // task ID → cancel, for tasks running here
}

func New(s store.Store, e Executor, workers int, log *slog.Logger) *Pool {
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
	t := task.Task{ID: rec.ID, Repo: rec.Repo, Prompt: rec.Prompt, ModelID: rec.Model.ID, PR: rec.PR.Requested, ParentID: rec.ParentID}
	var res task.Result
	var err error
	if t.ParentID != "" {
		t.FollowUp, err = p.followUp(taskCtx, rec)
	}
	if err == nil {
		res, err = p.exec.Run(taskCtx, t)
	}
	// Untrack before saving: once the store says the task is finished,
	// Cancel must already report false.
	p.setRunning(rec.ID, nil)
	o := outcome(taskCtx, res, err)
	if rec.PR.Requested && o.State != task.Succeeded && o.PRError == "" {
		o.PRError = fmt.Sprintf("no pull request: the task %s", o.State)
	}

	// taskCtx may be cancelled already; recording the outcome must still happen.
	saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelSave()
	if err := p.store.Finish(saveCtx, rec.ID, o); err != nil {
		log.Error("recording outcome", "id", rec.ID, "err", err)
		return
	}
	log.Info("task finished", "id", rec.ID, "state", o.State, "err", o.Error)
}

// followUp gathers what a follow-up needs from its thread: the pull
// request, where the thread started, the latest summary and the earlier
// requests.
func (p *Pool) followUp(ctx context.Context, rec store.Record) (*task.FollowUp, error) {
	thread, err := p.store.ThreadTasks(ctx, rec.ThreadID)
	if err != nil {
		return nil, err
	}
	if rec.PR.Branch == "" || rec.PR.Number == 0 || len(thread) == 0 {
		return nil, fmt.Errorf("thread %s has no pull request to continue", rec.ThreadID)
	}
	fu := &task.FollowUp{Branch: rec.PR.Branch, PRNumber: rec.PR.Number, ThreadBase: thread[0].BaseCommit}
	for _, prev := range thread {
		if prev.ID == rec.ID {
			break
		}
		fu.Requests = append(fu.Requests, prev.Prompt)
		if prev.Summary != "" {
			fu.Summary = prev.Summary // the latest one wins: it covers the whole thread
		}
	}
	return fu, nil
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
	o := store.Outcome{State: task.Succeeded, LogPath: res.LogPath, PatchPath: res.PatchPath,
		BaseCommit: res.BaseCommit, Summary: res.Summary, PRURL: res.PRURL, PRNumber: res.PRNumber,
		PRBranch: res.PRBranch, PRError: res.PRError}
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
