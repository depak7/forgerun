// Package store persists tasks. Backends live in sub-packages (store/postgres).
package store

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/task"
)

// ErrNotFound is returned when no task has the given ID.
var ErrNotFound = errors.New("task not found")

// ErrAgentNotFound is returned when no agent has the given ID.
var ErrAgentNotFound = errors.New("agent not found")

// ErrSnapshotNotFound is returned when there is no such snapshot, or no
// current one for a repo and agent (meaning: start the task cold).
var ErrSnapshotNotFound = errors.New("snapshot not found")

// Agent is a coding agent forgerun can run (a row of the agents table).
type Agent struct {
	ID             string // e.g. "cursor"
	Name           string // e.g. "Cursor"
	Enabled        bool
	DefaultModelID string // used when a request names no model; "" if none
}

// Record is a task as stored: what was asked, and what happened.
type Record struct {
	ID         string
	Repo       string
	Prompt     string
	AgentID    string
	Model      models.Variant // the model the task runs on (from the models table)
	State      task.State
	Error      string // "" unless Failed
	LogPath    string
	PatchPath  string // "" if no changes
	SnapshotID *int64 // the snapshot the task was restored from; nil = cold start
	CreatedAt  time.Time
	StartedAt  *time.Time // nil until running
	FinishedAt *time.Time // nil until terminal
}

// SnapshotState is where a snapshot is in its life.
type SnapshotState string

const (
	SnapshotCurrent SnapshotState = "current" // new tasks for its repo and agent restore from it
	SnapshotRetired SnapshotState = "retired" // replaced; still on disk until deleted
	SnapshotDeleted SnapshotState = "deleted" // gone from disk; the row is kept for task history
)

// Snapshot is a saved VM disk with a repo checked out and its dependencies
// installed, for one agent. Each (repo, agent) has at most one current
// snapshot, which is replaced after every successful task.
type Snapshot struct {
	ID              int64
	Repo            string // normalized, see NormalizeRepo
	AgentID         string
	Ref             string // what sandbox.Restore takes
	Commit          string // the commit the repo was reset to before snapshotting
	DepsFingerprint string // hash of the lockfiles it was installed from; "" if none
	SizeBytes       int64  // real disk usage
	State           SnapshotState
	CreatedAt       time.Time
	LastUsedAt      time.Time  // last time a task restored from it
	DeletedAt       *time.Time // nil until deleted
}

// NormalizeRepo returns the key snapshots are stored under, so different
// spellings of one repo share a snapshot: the scheme and host are lower-cased
// and a trailing "/" or ".git" is dropped. The path keeps its case, since
// some hosts treat it as case-sensitive.
func NormalizeRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	if u, err := url.Parse(repo); err == nil && u.Host != "" {
		u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
		repo = u.String()
	}
	repo = strings.TrimRight(repo, "/")
	return strings.TrimSuffix(repo, ".git")
}

// Outcome is how a task ended.
type Outcome struct {
	State     task.State // Succeeded, Failed or Cancelled
	Error     string
	LogPath   string
	PatchPath string
}

type Store interface {
	// Create saves a new task in the Queued state for agentID. An empty
	// t.ModelID means the agent's default model.
	Create(ctx context.Context, t task.Task, agentID string) error
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
	// GetAgent returns one agent, or ErrAgentNotFound.
	GetAgent(ctx context.Context, id string) (Agent, error)
	// ListAgents returns every enabled agent, sorted by id.
	ListAgents(ctx context.Context) ([]Agent, error)
	// ListModels returns every enabled model of every enabled agent.
	ListModels(ctx context.Context) ([]models.Variant, error)

	// CurrentSnapshot returns the snapshot new tasks for repo and agentID
	// should restore from, or ErrSnapshotNotFound.
	CurrentSnapshot(ctx context.Context, repo, agentID string) (Snapshot, error)
	// UseSnapshot records that a task was restored from a snapshot and
	// updates the snapshot's LastUsedAt.
	UseSnapshot(ctx context.Context, taskID string, snapshotID int64) error
	// ReplaceSnapshot saves snap (Repo, AgentID, Ref, Commit,
	// DepsFingerprint, SizeBytes) as the current snapshot for its repo and
	// agent, and retires the previous one, atomically. It returns the saved
	// snapshot and the retired one (nil if there was none), which the caller
	// deletes from disk and then passes to MarkSnapshotDeleted.
	ReplaceSnapshot(ctx context.Context, snap Snapshot) (saved Snapshot, retired *Snapshot, err error)
	// MarkSnapshotDeleted records that a snapshot is gone from disk.
	// Marking it twice is not an error.
	MarkSnapshotDeleted(ctx context.Context, id int64) error
	// ListSnapshots returns every snapshot not yet deleted, least recently
	// used first.
	ListSnapshots(ctx context.Context) ([]Snapshot, error)

	// Ping reports whether the store is reachable.
	Ping(ctx context.Context) error
	Close()
}
