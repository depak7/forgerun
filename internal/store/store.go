// Package store persists tasks. Backends live in sub-packages (store/postgres).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/depak7/forgerun/internal/task"
)

// ErrNotFound is returned when no task has the given ID.
var ErrNotFound = errors.New("task not found")

// Record is a task as stored: what was asked, and what happened.
type Record struct {
	ID         string
	Repo       string
	Prompt     string
	Agent      string
	State      task.State
	Error      string // "" unless Failed
	LogPath    string
	PatchPath  string // "" if no changes
	CreatedAt  time.Time
	StartedAt  *time.Time // nil until running
	FinishedAt *time.Time // nil until terminal
}

// Outcome is how a task ended.
type Outcome struct {
	State     task.State // Succeeded, Failed or Cancelled
	Error     string
	LogPath   string
	PatchPath string
}

type Store interface {
	// Create saves a new task in the Queued state.
	Create(ctx context.Context, t task.Task, agent string) error
	// Start moves a task from Queued to Running.
	Start(ctx context.Context, id string) error
	// Finish moves a task from Running to a terminal state.
	Finish(ctx context.Context, id string, o Outcome) error
	// Claim atomically moves the oldest Queued task to Running and returns it.
	// ok is false when the queue is empty. Safe to call from many workers.
	Claim(ctx context.Context) (rec Record, ok bool, err error)
	// Cancel moves a Queued task to Cancelled. Running tasks are cancelled by
	// their worker instead, which then calls Finish.
	Cancel(ctx context.Context, id string) error
	// FailRunning marks every Running task as Failed with reason. Used at
	// startup to recover tasks orphaned by a crash (single-instance only).
	FailRunning(ctx context.Context, reason string) (int64, error)
	Get(ctx context.Context, id string) (Record, error)
	// List returns the most recent tasks first.
	List(ctx context.Context, limit int) ([]Record, error)
	// Ping reports whether the store is reachable.
	Ping(ctx context.Context) error
	Close()
}
