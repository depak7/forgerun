package sandbox

import (
	"context"
	"fmt"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

// snapshotGroup is the microsandbox snapshot group forgerun stores all of its
// snapshots in, so they're easy to find and clean up.
const snapshotGroup = "forgerun"

// Snapshot is a saved VM disk that new VMs can be restored from. It never
// contains secrets: microsandbox keeps them outside the VM and doesn't
// carry them into snapshots.
type Snapshot struct {
	Ref string // what Restore and DeleteSnapshot take
}

// TakeSnapshot saves the VM's disk as a snapshot called name. The VM keeps
// running; its file system is flushed first so recent writes are included.
// (forgerun VMs are ephemeral, so stopping one first would delete its disk.)
func (s *Sandbox) TakeSnapshot(ctx context.Context, name string) (Snapshot, error) {
	a, err := microsandbox.Snapshot.Create(ctx, microsandbox.SnapshotCreateOptions{
		FromSandbox: s.Name(),
		Name:        name,
		Group:       snapshotGroup,
		GuestFlush:  microsandbox.GuestFlushRequired,
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("taking snapshot %s of %s: %w", name, s.Name(), err)
	}
	// a.SizeBytes() is the virtual disk size (e.g. 4 GiB), not space used,
	// so it isn't exposed; measure real usage on disk if it's ever needed.
	return Snapshot{Ref: a.Reference()}, nil
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
func DeleteSnapshot(ctx context.Context, ref string) error {
	err := microsandbox.Snapshot.Remove(ctx, ref, false)
	if err != nil && !microsandbox.IsKind(err, microsandbox.ErrSnapshotNotFound) {
		return fmt.Errorf("deleting snapshot %s: %w", ref, err)
	}
	return nil
}
