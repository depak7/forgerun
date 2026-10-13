package task

import (
	"context"

	"github.com/depak7/forgerun/internal/sandbox"
)

// Snapshotter saves a successful task's VM as its repo's snapshot, so later
// tasks can start from it. snapshots.Manager implements it; it's declared
// here, where it's used, so this package doesn't depend on the store.
type Snapshotter interface {
	Save(ctx context.Context, sb *sandbox.Sandbox, req SnapshotRequest, logf func(format string, a ...any)) error
}

// SnapshotRequest says which VM state to keep and how to clean it first.
type SnapshotRequest struct {
	TaskID     string
	Repo       string
	AgentID    string
	RepoDir    string   // checkout inside the VM
	StatePaths []string // agent leftovers to delete before snapshotting
}
