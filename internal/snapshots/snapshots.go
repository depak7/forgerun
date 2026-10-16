// Package snapshots keeps one VM snapshot per repo and agent, so tasks can
// start from a checkout with dependencies already installed. After each
// successful task, that task's VM (cleaned) replaces the repo's snapshot.
package snapshots

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// Store keeps the record of snapshots: forgerun's database on an
// all-in-one server (store.Store is one), or a file on a worker machine,
// since snapshots live on the machine that made them.
type Store interface {
	CurrentSnapshot(ctx context.Context, repo, agentID string) (store.Snapshot, error)
	UseSnapshot(ctx context.Context, taskID string, snapshotID int64) error
	ReplaceSnapshot(ctx context.Context, snap store.Snapshot) (saved store.Snapshot, retired *store.Snapshot, err error)
	MarkSnapshotDeleted(ctx context.Context, id int64) error
	ListSnapshots(ctx context.Context) ([]store.Snapshot, error)
}

var _ Store = (store.Store)(nil)

// Manager saves, restores and cleans up snapshots. It is safe for
// concurrent use.
type Manager struct {
	Store Store
	// Prefix starts the name of every snapshot group this manager owns
	// ("" = "forgerun"). Sweep only ever touches groups with its prefix,
	// so tests use their own and can't delete a server's snapshots.
	Prefix string
	Limits Limits
}

var _ task.Snapshotter = (*Manager)(nil)

// Restore boots a VM from the current snapshot for req's repo and agent,
// attaches req.Config's secrets, updates the code to the latest commit and
// records that the task started from it.
func (m *Manager) Restore(ctx context.Context, req task.RestoreRequest, logf func(string, ...any)) (*sandbox.Sandbox, error) {
	snap, err := m.Store.CurrentSnapshot(ctx, req.Repo, req.AgentID)
	if errors.Is(err, store.ErrSnapshotNotFound) {
		return nil, task.ErrNoSnapshot
	}
	if err != nil {
		return nil, err
	}

	sb, err := sandbox.Restore(ctx, snap.Ref, req.TaskID, req.Config)
	if err != nil {
		// The snapshot itself is unusable (e.g. missing or corrupt). Remove
		// it so later tasks don't trip over it too; the next successful
		// task saves a fresh one.
		if rmErr := m.remove(ctx, snap); rmErr != nil {
			logf("warning: removing unusable snapshot %d: %v", snap.ID, rmErr)
		}
		return nil, fmt.Errorf("restoring snapshot %d: %w", snap.ID, err)
	}
	logf("restored from snapshot %d (commit %.7s, taken %s ago)", snap.ID, snap.Commit,
		time.Since(snap.CreatedAt).Round(time.Second))

	// A failure here (e.g. the network) doesn't mean the snapshot is bad,
	// so it's kept; only this VM is discarded.
	commit, err := update(ctx, sb, req)
	if err != nil {
		sb.Destroy()
		return nil, err
	}
	logf("updated to %.7s", commit)

	if err := m.Store.UseSnapshot(ctx, req.TaskID, snap.ID); err != nil {
		logf("warning: recording snapshot use: %v", err) // the VM is fine; carry on
	}
	return sb, nil
}

// Save cleans the task's VM, snapshots it as the current snapshot for its
// repo and agent, and deletes the snapshot it replaces.
func (m *Manager) Save(ctx context.Context, sb *sandbox.Sandbox, req task.SnapshotRequest, logf func(string, ...any)) error {
	logf("cleaning VM for snapshot")
	commit, err := clean(ctx, sb, req)
	if err != nil {
		return err
	}
	used, err := usedBytes(ctx, sb)
	if err != nil {
		logf("warning: measuring the VM's files: %v", err) // only the bloat check is skipped
	}

	before, after, err := sb.Compact(ctx)
	if err != nil {
		return err
	}
	snap, err := sb.TakeSnapshot(ctx, m.Group(req.Repo, req.AgentID), req.TaskID)
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
	if used > 0 && bloated(saved.SizeBytes, used) {
		// Space freed inside the VM is never given back by the disk files,
		// so a rolling snapshot only grows. Drop it: the next task starts
		// cold and saves a compact one.
		if err := m.remove(ctx, saved); err != nil {
			logf("warning: snapshot %d is bloated but couldn't be dropped: %v", saved.ID, err)
		} else {
			logf("snapshot %d dropped: %s on disk for %s of files; the next task starts fresh", saved.ID, size(saved.SizeBytes), size(used))
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
	commit, err := resetToDefault(ctx, sb, req.RepoDir)
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

// update brings a restored checkout to the latest commit of the remote's
// default branch, like a git pull that can't conflict. It returns the commit.
func update(ctx context.Context, sb *sandbox.Sandbox, req task.RestoreRequest) (string, error) {
	dir := req.RepoDir
	if req.Fetch != nil { // a private repo: the host fetches, the VM has no credentials
		if _, err := task.HostFetch(ctx, sb, req.Fetch, req.Repo, "", dir); err != nil {
			return "", err
		}
		return resetToDefault(ctx, sb, dir)
	}
	// Ask the remote which branch is the default, in case it changed
	// (e.g. master renamed to main) since the snapshot was taken.
	if _, err := git(ctx, sb, dir, "remote", "set-head", "origin", "--auto"); err != nil {
		return "", fmt.Errorf("finding default branch: %w", err)
	}
	remote, err := git(ctx, sb, dir, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("finding default branch: %w", err)
	}
	// Only the newest commit is needed, as with the cold path's shallow clone.
	if _, err := git(ctx, sb, dir, "fetch", "--depth=1", "origin", strings.TrimPrefix(remote, "origin/")); err != nil {
		return "", fmt.Errorf("fetching %s: %w", remote, err)
	}
	return resetToDefault(ctx, sb, dir)
}

// resetToDefault checks out origin's default branch at origin's commit,
// discarding local changes, commits, other branches, stashes and untracked
// files (ignored files are kept). It returns the commit.
func resetToDefault(ctx context.Context, sb *sandbox.Sandbox, dir string) (string, error) {
	// origin/HEAD is the remote's default branch, e.g. "origin/main".
	remote, err := git(ctx, sb, dir, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("finding default branch: %w", err)
	}
	branch := strings.TrimPrefix(remote, "origin/")
	// -B (re)creates the branch at origin's commit and --force throws away
	// local changes, so even an agent that committed or switched branches
	// is undone.
	if _, err := git(ctx, sb, dir, "checkout", "--force", "-B", branch, remote); err != nil {
		return "", fmt.Errorf("resetting to %s: %w", remote, err)
	}
	// Without -x, clean leaves ignored files alone.
	if _, err := git(ctx, sb, dir, "clean", "-fd"); err != nil {
		return "", fmt.Errorf("removing untracked files: %w", err)
	}
	// Branches and stashes the agent made would otherwise reach the next task.
	branches, err := git(ctx, sb, dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return "", fmt.Errorf("listing branches: %w", err)
	}
	for _, b := range strings.Fields(branches) {
		if b == branch {
			continue
		}
		if _, err := git(ctx, sb, dir, "branch", "-D", b); err != nil {
			return "", fmt.Errorf("deleting branch %s: %w", b, err)
		}
	}
	if _, err := git(ctx, sb, dir, "stash", "clear"); err != nil {
		return "", fmt.Errorf("clearing stashes: %w", err)
	}
	return git(ctx, sb, dir, "rev-parse", "HEAD")
}

// git runs git in dir inside the VM and returns its trimmed output.
func git(ctx context.Context, sb *sandbox.Sandbox, dir string, args ...string) (string, error) {
	out, err := sb.Run(ctx, sandbox.Cmd{Name: "git", Args: append([]string{"-C", dir}, args...)})
	return strings.TrimSpace(out), err
}

// Group is the microsandbox snapshot group for a repo and agent, e.g.
// "forgerun-cursor-hello-world-1a2b3c4d5e6f". Each (repo, agent) gets its
// own group because a group is one lineage with one head (its current
// snapshot). The hash keeps similar names apart; the rest is for humans
// looking in ~/.microsandbox/snapshots.
func (m *Manager) Group(repo, agentID string) string {
	repo = store.NormalizeRepo(repo)
	sum := sha256.Sum256([]byte(repo + " " + agentID))
	return fmt.Sprintf("%s-%s-%s-%s", m.prefix(), slug(agentID), slug(path.Base(repo)), hex.EncodeToString(sum[:6]))
}

func (m *Manager) prefix() string { return cmp.Or(m.Prefix, "forgerun") }

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

// bloatSlack is disk a snapshot may use beyond twice its files before it's
// replaced: a fresh snapshot already carries filesystem overhead (about
// 15 MB on a small repo), and a cold start shouldn't be forced for little.
const bloatSlack = 64 << 20

// bloated reports whether a snapshot of sizeBytes on disk holds so little
// live data (usedBytes) that starting fresh is better than keeping it.
func bloated(sizeBytes, usedBytes int64) bool {
	return sizeBytes > 2*usedBytes+bloatSlack
}

// usedBytes is the space the VM's files really use, as df reports for its
// root filesystem.
func usedBytes(ctx context.Context, sb *sandbox.Sandbox) (int64, error) {
	out, err := sb.Run(ctx, sandbox.Cmd{Name: "df", Args: []string{"-B1", "--output=used", "/"}})
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(out) // "Used" then the number
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected df output %q", out)
	}
	var n int64
	if _, err := fmt.Sscan(fields[len(fields)-1], &n); err != nil {
		return 0, fmt.Errorf("unexpected df output %q", out)
	}
	return n, nil
}

// size formats bytes for the log, e.g. "39.2 MB" or "3.1 GB".
func size(b int64) string {
	if b >= 1e9 {
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	}
	return fmt.Sprintf("%.1f MB", float64(b)/1e6)
}
