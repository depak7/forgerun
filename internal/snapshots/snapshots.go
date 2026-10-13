// Package snapshots keeps one VM snapshot per repo and agent, so tasks can
// start from a checkout with dependencies already installed. After each
// successful task, that task's VM (cleaned) replaces the repo's snapshot.
package snapshots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// Manager saves and replaces snapshots. It is safe for concurrent use.
type Manager struct {
	Store store.Store
}

var _ task.Snapshotter = (*Manager)(nil)

// Save cleans the task's VM, snapshots it as the current snapshot for its
// repo and agent, and deletes the snapshot it replaces.
func (m *Manager) Save(ctx context.Context, sb *sandbox.Sandbox, req task.SnapshotRequest, logf func(string, ...any)) error {
	logf("cleaning VM for snapshot")
	commit, err := clean(ctx, sb, req)
	if err != nil {
		return err
	}

	before, after, err := sb.Compact(ctx)
	if err != nil {
		return err
	}
	snap, err := sb.TakeSnapshot(ctx, Group(req.Repo, req.AgentID), req.TaskID)
	if err != nil {
		return err
	}

	saved, retired, err := m.Store.ReplaceSnapshot(ctx, store.Snapshot{
		Repo: req.Repo, AgentID: req.AgentID, Ref: snap.Ref, Commit: commit, SizeBytes: snap.SizeBytes,
	})
	if err != nil {
		// Not recorded, so nothing would ever clean it up: remove it now.
		if delErr := sandbox.DeleteSnapshot(ctx, snap.Ref); delErr != nil {
			logf("warning: removing unrecorded snapshot %s: %v", snap.Ref, delErr)
		}
		return err
	}
	logf("snapshot %d saved at %.7s (%s, compacted %d -> %d layers)", saved.ID, commit, size(saved.SizeBytes), before, after)

	if retired != nil {
		if err := m.remove(ctx, *retired); err != nil {
			// The row stays "retired", so a later sweep can retry.
			logf("warning: previous snapshot %d not deleted: %v", retired.ID, err)
		} else {
			logf("previous snapshot %d deleted", retired.ID)
		}
	}
	return nil
}

// remove deletes a snapshot from disk, then records that it's gone. In that
// order: a crash in between leaves a row pointing at nothing, which is
// harmless, rather than files no row knows about.
func (m *Manager) remove(ctx context.Context, snap store.Snapshot) error {
	if err := sandbox.DeleteSnapshot(ctx, snap.Ref); err != nil {
		return err
	}
	return m.Store.MarkSnapshotDeleted(ctx, snap.ID)
}

// clean resets the VM so the next task starts as if from a fresh clone of
// the default branch, keeping only what makes starting fast. It returns the
// commit the repo was reset to.
//
// Kept: git-ignored files (node_modules, build caches). Removed: the agent's
// edits, commits and branches, untracked files, and the agent's state paths.
func clean(ctx context.Context, sb *sandbox.Sandbox, req task.SnapshotRequest) (string, error) {
	git := func(args ...string) (string, error) {
		out, err := sb.Run(ctx, sandbox.Cmd{Name: "git", Args: append([]string{"-C", req.RepoDir}, args...)})
		return strings.TrimSpace(out), err
	}
	// origin/HEAD is the remote's default branch, e.g. "origin/main".
	remote, err := git("rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("finding default branch: %w", err)
	}
	branch := strings.TrimPrefix(remote, "origin/")
	// -B (re)creates the branch at origin's commit and --force throws away
	// local changes, so even an agent that committed or switched branches
	// is undone.
	if _, err := git("checkout", "--force", "-B", branch, remote); err != nil {
		return "", fmt.Errorf("resetting to %s: %w", remote, err)
	}
	// Without -x, clean leaves ignored files alone.
	if _, err := git("clean", "-fd"); err != nil {
		return "", fmt.Errorf("removing untracked files: %w", err)
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if len(req.StatePaths) > 0 {
		args := append([]string{"-rf", "--"}, req.StatePaths...)
		if _, err := sb.Run(ctx, sandbox.Cmd{Name: "rm", Args: args}); err != nil {
			return "", fmt.Errorf("removing agent state: %w", err)
		}
	}
	return commit, nil
}

// Group is the microsandbox snapshot group for a repo and agent, e.g.
// "forgerun-cursor-hello-world-1a2b3c4d5e6f". Each (repo, agent) gets its
// own group because a group is one lineage with one head (its current
// snapshot). The hash keeps similar names apart; the rest is for humans
// looking in ~/.microsandbox/snapshots.
func Group(repo, agentID string) string {
	repo = store.NormalizeRepo(repo)
	sum := sha256.Sum256([]byte(repo + " " + agentID))
	return fmt.Sprintf("forgerun-%s-%s-%s", slug(agentID), slug(path.Base(repo)), hex.EncodeToString(sum[:6]))
}

// slug keeps lower-case letters and digits, turning anything else into
// single dashes: "Hello_World.js" -> "hello-world-js".
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// size formats bytes for the log, e.g. "39.2 MB".
func size(b int64) string {
	return fmt.Sprintf("%.1f MB", float64(b)/1e6)
}
