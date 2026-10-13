package task

import (
	"context"
	"errors"

	"github.com/depak7/forgerun/internal/sandbox"
)

// ErrNoSnapshot means the repo has no snapshot for this agent yet; the task
// simply starts cold.
var ErrNoSnapshot = errors.New("no snapshot")

// Snapshotter starts tasks from their repo's snapshot and saves successful
// tasks' VMs as the new snapshot. snapshots.Manager implements it; it's
// declared here, where it's used, so this package doesn't depend on the store.
type Snapshotter interface {
	// Restore boots a VM from the repo's current snapshot, with the code
	// updated to the latest commit of the default branch. It returns
	// ErrNoSnapshot if there is none; any other error means the snapshot
	// couldn't be used. On error, no VM is left running.
	Restore(ctx context.Context, req RestoreRequest, logf func(format string, a ...any)) (*sandbox.Sandbox, error)
	Save(ctx context.Context, sb *sandbox.Sandbox, req SnapshotRequest, logf func(format string, a ...any)) error
}

// RestoreRequest says which repo's snapshot to start a task from.
type RestoreRequest struct {
	TaskID  string // also the new VM's name
	Repo    string
	AgentID string
	RepoDir string         // checkout inside the VM
	Config  sandbox.Config // CPUs, memory, lifetime and secrets for the new VM
}

// SnapshotRequest says which VM state to keep and how to clean it first.
type SnapshotRequest struct {
	TaskID     string
	Repo       string
	AgentID    string
	RepoDir    string   // checkout inside the VM
	StatePaths []string // agent leftovers to delete before snapshotting
}
