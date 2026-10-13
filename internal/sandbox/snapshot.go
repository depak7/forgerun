package sandbox

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

// Snapshot is a saved VM disk that new VMs can be restored from. It never
// contains secrets: microsandbox keeps them outside the VM and doesn't
// carry them into snapshots.
type Snapshot struct {
	Ref       string // what Restore and DeleteSnapshot take
	SizeBytes int64  // real disk space used; 0 if it couldn't be measured
}

// TakeSnapshot saves the VM's disk as snapshot name in group, and makes it
// the group's head. The VM keeps running; its file system is flushed first
// so recent writes are included. (forgerun VMs are ephemeral, so stopping
// one first would delete its disk.)
//
// A microsandbox group is one lineage of snapshots, and its head can't be
// deleted while other members exist. Making each new snapshot the head lets
// the one it replaces be deleted.
func (s *Sandbox) TakeSnapshot(ctx context.Context, group, name string) (Snapshot, error) {
	a, err := microsandbox.Snapshot.Create(ctx, microsandbox.SnapshotCreateOptions{
		FromSandbox: s.Name(),
		Name:        name,
		Group:       group,
		GuestFlush:  microsandbox.GuestFlushRequired,
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("taking snapshot %s of %s: %w", name, s.Name(), err)
	}
	if _, err := microsandbox.Snapshot.GroupHead(ctx, group+":"+name); err != nil {
		DeleteSnapshot(ctx, a.Reference())
		return Snapshot{}, fmt.Errorf("making snapshot %s the head of %s: %w", name, group, err)
	}
	// a.SizeBytes() is the virtual disk size (e.g. 4 GiB), not space used,
	// so measure the snapshot's folder instead. Local snapshots are
	// referenced by their folder path.
	snap := Snapshot{Ref: a.Reference()}
	if a.ReferenceKind() == "path" {
		snap.SizeBytes, err = diskUsage(a.Reference())
		if err != nil {
			return snap, fmt.Errorf("measuring snapshot %s: %w", name, err)
		}
	}
	return snap, nil
}

// diskUsage returns the space the files under dir actually occupy, like du.
// VM disks are sparse files, so their apparent size overstates usage;
// allocated blocks are counted instead.
func diskUsage(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			total += st.Blocks * 512 // st_blocks is in 512-byte units on macOS and Linux
		} else {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// Compact merges the VM's disk layers. Each snapshot of a restored VM adds
// a layer, so compacting before snapshotting keeps rolling snapshots from
// stacking layers without limit. The VM keeps running (paused for a few
// milliseconds). It returns the layer counts before and after.
func (s *Sandbox) Compact(ctx context.Context) (before, after int, err error) {
	res, err := s.vm.Compact(ctx, microsandbox.DiskCompactionOptions{RootDiskOnly: true})
	if err != nil {
		return 0, 0, fmt.Errorf("compacting %s: %w", s.Name(), err)
	}
	return int(res.InputLayers), int(res.OutputLayers), nil
}

// Restore boots a new VM called name from a snapshot. cfg.CPUs, MemoryMiB,
// MaxLifetime and Secrets apply; cfg.Image is ignored (the snapshot's disk is
// used). The caller must call Destroy when done, as with New.
func Restore(ctx context.Context, ref, name string, cfg Config) (*Sandbox, error) {
	vm, err := microsandbox.RestoreSandbox(ctx, ref, name,
		microsandbox.WithRestoreCPUs(cfg.CPUs),
		microsandbox.WithRestoreMemory(cfg.MemoryMiB),
		microsandbox.WithRestoreMaxDuration(cfg.MaxLifetime),
	)
	if err != nil {
		return nil, fmt.Errorf("restoring %s from snapshot %s: %w", name, ref, err)
	}
	sb := &Sandbox{vm: vm}
	if len(cfg.Secrets) > 0 {
		if err := sb.attachSecrets(ctx, cfg.Secrets); err != nil {
			sb.Destroy()
			return nil, err
		}
	}
	return sb, nil
}

// attachSecrets adds secrets to a restored VM. Restores never carry secrets
// over, and a running VM only picks up new secrets on restart, so this
// restarts it; the restored disk is kept.
func (s *Sandbox) attachSecrets(ctx context.Context, secrets []Secret) error {
	specs := make(map[string]microsandbox.SecretModifySpec, len(secrets))
	for _, sec := range secrets {
		specs[sec.EnvVar] = microsandbox.SecretModifySpec{Value: sec.Value, AllowedHosts: sec.AllowHosts}
	}
	_, err := s.vm.Modify(ctx, microsandbox.ModifyOptions{
		Secrets: specs,
		Policy:  microsandbox.ModificationPolicyRestart,
	})
	if err != nil {
		return fmt.Errorf("attaching secrets to %s: %w", s.Name(), err)
	}
	return nil
}

// DeleteSnapshot removes a snapshot. Deleting one that no longer exists is
// not an error.
//
// It always forces: microsandbox refuses to delete a snapshot that others
// were taken from, but each snapshot is self-contained and running VMs don't
// need their source (verified by experiment), and forgerun only deletes
// snapshots it has replaced or evicted.
func DeleteSnapshot(ctx context.Context, ref string) error {
	err := microsandbox.Snapshot.Remove(ctx, ref, true)
	if err != nil && !microsandbox.IsKind(err, microsandbox.ErrSnapshotNotFound) {
		return fmt.Errorf("deleting snapshot %s: %w", ref, err)
	}
	return nil
}
