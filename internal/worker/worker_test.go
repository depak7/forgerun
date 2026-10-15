package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
)

// fakeDefault is the default model of the "fake" agent the tests create tasks for.
var fakeDefault = models.Variant{ID: "auto", AgentID: "fake", Family: "auto", IsDefault: true, AgentDefault: true, ZDR: true}

// fakeExec lets each test define Run inline.
type fakeExec func(ctx context.Context, t task.Task) (task.Result, error)

func (f fakeExec) Run(ctx context.Context, t task.Task) (task.Result, error) { return f(ctx, t) }

// blockUntilCancelled behaves like a long agent run that stops when ctx ends.
func blockUntilCancelled(ctx context.Context, _ task.Task) (task.Result, error) {
	<-ctx.Done()
	return task.Result{LogPath: "x.log"}, ctx.Err()
}

// startPool runs a pool in the background. stop cancels it and waits for Run to return.
func startPool(t *testing.T, s store.Store, e Executor, workers int, poll time.Duration) (p *Pool, stop func()) {
	t.Helper()
	p = New(s, e, workers, slog.New(slog.DiscardHandler))
	p.poll = poll
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("pool did not stop within 2s")
		}
	}
	t.Cleanup(stop) // calling stop twice is fine: cancel and a closed channel are idempotent
	return p, stop
}

func create(t *testing.T, s store.Store) task.Task {
	t.Helper()
	tk := task.New("https://example.com/r.git", "p")
	if err := s.Create(context.Background(), tk, "fake"); err != nil {
		t.Fatal(err)
	}
	return tk
}

// waitState polls until the task reaches want, or fails after 2s.
func waitState(t *testing.T, s store.Store, id string, want task.State) store.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.State == want {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s is %s, want %s", id, rec.State, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSuccess(t *testing.T) {
	s := memory.New(fakeDefault)
	tk := create(t, s)
	startPool(t, s, fakeExec(func(context.Context, task.Task) (task.Result, error) {
		return task.Result{LogPath: "a.log", Diff: "diff --git a/x b/x\n", Reply: "Done.", BaseCommit: "abc"}, nil
	}), 1, 10*time.Millisecond)

	rec := waitState(t, s, tk.ID, task.Succeeded)
	if !rec.HasDiff || rec.LogPath != "a.log" || rec.Error != "" || rec.Reply != "Done." || rec.BaseCommit != "abc" {
		t.Errorf("got %+v", rec)
	}
	if diff, err := s.Diff(context.Background(), tk.ID); err != nil || diff != "diff --git a/x b/x\n" {
		t.Errorf("diff %q, %v", diff, err)
	}
}

func TestFailure(t *testing.T) {
	s := memory.New(fakeDefault)
	tk := create(t, s)
	startPool(t, s, fakeExec(func(context.Context, task.Task) (task.Result, error) {
		return task.Result{}, errors.New("clone: exit code 128")
	}), 1, 10*time.Millisecond)

	rec := waitState(t, s, tk.ID, task.Failed)
	if rec.Error != "clone: exit code 128" {
		t.Errorf("Error = %q", rec.Error)
	}
}

func TestUserCancel(t *testing.T) {
	s := memory.New(fakeDefault)
	tk := create(t, s)
	p, _ := startPool(t, s, fakeExec(blockUntilCancelled), 1, 10*time.Millisecond)

	waitState(t, s, tk.ID, task.Running)
	if !p.Cancel(tk.ID) {
		t.Fatal("Cancel returned false for a running task")
	}
	rec := waitState(t, s, tk.ID, task.Cancelled)
	if rec.Error != "cancelled by user" {
		t.Errorf("Error = %q", rec.Error)
	}
	if p.Cancel(tk.ID) {
		t.Error("Cancel returned true for a task that is no longer running")
	}
}

func TestShutdownFailsRunningTask(t *testing.T) {
	s := memory.New(fakeDefault)
	tk := create(t, s)
	_, stop := startPool(t, s, fakeExec(blockUntilCancelled), 1, 10*time.Millisecond)

	waitState(t, s, tk.ID, task.Running)
	stop() // returns only after the worker recorded the outcome
	rec, _ := s.Get(context.Background(), tk.ID)
	if rec.State != task.Failed || rec.Error != "server shut down" {
		t.Errorf("after shutdown: state=%s error=%q", rec.State, rec.Error)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	s := memory.New(fakeDefault)
	release := make(chan struct{})
	startPool(t, s, fakeExec(func(ctx context.Context, _ task.Task) (task.Result, error) {
		select {
		case <-release:
			return task.Result{}, nil
		case <-ctx.Done():
			return task.Result{}, ctx.Err()
		}
	}), 2, 10*time.Millisecond)

	var ids []string
	for range 3 {
		ids = append(ids, create(t, s).ID)
		time.Sleep(2 * time.Millisecond) // distinct created_at: oldest-first is deterministic
	}
	waitState(t, s, ids[0], task.Running)
	waitState(t, s, ids[1], task.Running)
	time.Sleep(50 * time.Millisecond) // give a hypothetical third worker time to misbehave
	if rec, _ := s.Get(context.Background(), ids[2]); rec.State != task.Queued {
		t.Fatalf("third task is %s with 2 workers, want queued", rec.State)
	}

	close(release) // every blocked and future Run returns immediately
	for _, id := range ids {
		waitState(t, s, id, task.Succeeded)
	}
}

func TestNudgeWakesIdleWorker(t *testing.T) {
	s := memory.New(fakeDefault)
	p, _ := startPool(t, s, fakeExec(func(context.Context, task.Task) (task.Result, error) {
		return task.Result{}, nil
	}), 1, time.Hour) // without a nudge, the worker would sleep for an hour
	time.Sleep(50 * time.Millisecond) // let the worker find the queue empty and go idle

	tk := create(t, s)
	p.Nudge()
	waitState(t, s, tk.ID, task.Succeeded)
}

func TestOutcome(t *testing.T) {
	userCtx, userCancel := context.WithCancelCause(context.Background())
	userCancel(errCancelledByUser)

	parent, shutdown := context.WithCancel(context.Background())
	shutdownCtx, c := context.WithCancelCause(parent)
	defer c(nil)
	shutdown()

	live := context.Background()

	tests := []struct {
		name      string
		ctx       context.Context
		err       error
		wantState task.State
		wantError string
	}{
		{"success", live, nil, task.Succeeded, ""},
		{"user cancel", userCtx, context.Canceled, task.Cancelled, "cancelled by user"},
		{"shutdown", shutdownCtx, context.Canceled, task.Failed, "server shut down"},
		{"timeout", live, fmt.Errorf("agent: %w", context.DeadlineExceeded), task.Failed, "agent: context deadline exceeded"},
		{"plain failure", live, errors.New("boom"), task.Failed, "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := outcome(tc.ctx, task.Result{}, tc.err)
			if o.State != tc.wantState || o.Error != tc.wantError {
				t.Errorf("got (%s, %q), want (%s, %q)", o.State, o.Error, tc.wantState, tc.wantError)
			}
		})
	}
}

func TestPassesModelToExecutor(t *testing.T) {
	v := models.Variant{ID: "m-high", AgentID: "fake", Family: "m", Effort: "high", IsDefault: true, ZDR: true}
	s := memory.New(v)
	tk := task.New("https://example.com/r.git", "p")
	tk.ModelID = "m-high"
	if err := s.Create(context.Background(), tk, "fake"); err != nil {
		t.Fatal(err)
	}
	got := make(chan task.Task, 1)
	startPool(t, s, fakeExec(func(_ context.Context, tk task.Task) (task.Result, error) {
		got <- tk
		return task.Result{}, nil
	}), 1, 10*time.Millisecond)

	select {
	case tk := <-got:
		// Family and effort travel separately, for agents that take them as options.
		if tk.ModelID != "m-high" || tk.Family != "m" || tk.Effort != "high" {
			t.Errorf("executor got model %q (family %q, effort %q), want m-high (m, high)", tk.ModelID, tk.Family, tk.Effort)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("task never ran")
	}
}

// A follow-up gets what its session needs if it must be rebuilt: the
// commit the thread started from, and the earlier turns (prompts, replies,
// and the changes of the ones that succeeded), loaded only when asked.
func TestFollowUpContext(t *testing.T) {
	s, ctx := memory.New(fakeDefault), context.Background()
	finish := func(tk task.Task, o store.Outcome) {
		t.Helper()
		if err := s.Create(ctx, tk, "fake"); err != nil {
			t.Fatal(err)
		}
		s.Start(ctx, tk.ID)
		s.Finish(ctx, tk.ID, o)
	}
	first := task.New("r", "add greet")
	first.Plan = true // a plan step, then the build
	finish(first, store.Outcome{State: task.Succeeded, BaseCommit: "base0", Reply: "Added greet.", Diff: "+greet\n"})
	second := task.New("r", "add farewell")
	second.ParentID = first.ID
	finish(second, store.Outcome{State: task.Failed, Reply: "oops", Diff: "+half\n"}) // its changes were discarded
	third := task.New("r", "rename both")
	third.ParentID = second.ID
	if err := s.Create(ctx, third, "fake"); err != nil {
		t.Fatal(err)
	}

	got := make(chan task.Task, 1)
	startPool(t, s, fakeExec(func(_ context.Context, tk task.Task) (task.Result, error) {
		got <- tk
		return task.Result{}, nil
	}), 1, 10*time.Millisecond)

	select {
	case tk := <-got:
		if tk.ID != third.ID || tk.ThreadID != first.ID || tk.ParentID != second.ID || tk.ThreadBase != "base0" || tk.History == nil {
			t.Fatalf("follow-up = %+v", tk)
		}
		history, err := tk.History(ctx)
		want := []task.Turn{
			{Prompt: "add greet", Reply: "Added greet.", Diff: "+greet\n", Plan: true},
			{Prompt: "add farewell", Reply: "oops", Failed: true},
		}
		if err != nil || !reflect.DeepEqual(history, want) {
			t.Errorf("history = %+v, %v", history, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follow-up never ran")
	}
}
