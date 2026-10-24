package sandbox

import (
	"context"
	"io"
)

// Sandbox is a running sandbox: a microVM (microsandbox) or a gVisor
// container. Whatever runs inside sees its secrets only as placeholders.
type Sandbox interface {
	Name() string
	// Run executes c, waits for it, and returns its stdout. A non-zero exit
	// becomes an *ExitError.
	Run(ctx context.Context, c Cmd) (string, error)
	// Stream executes c, copying stdout and stderr to w as they arrive. A
	// non-zero exit becomes an *ExitError.
	Stream(ctx context.Context, c Cmd, w io.Writer) error
	// WriteFile writes data to path inside the sandbox, creating parent
	// directories.
	WriteFile(ctx context.Context, path string, data []byte) error
	// Destroy stops and removes the sandbox. It ignores any caller's
	// context: cleanup must still happen after a timeout or Ctrl-C.
	Destroy() error
	// TakeSnapshot saves the sandbox's disk as snapshot name in group, and
	// makes it the group's newest. The sandbox keeps running. Snapshots
	// never contain secrets.
	TakeSnapshot(ctx context.Context, group, name string) (Snapshot, error)
	// Compact merges the disk's layers before a snapshot, so rolling
	// snapshots don't stack layers without limit. It returns the layer
	// counts before and after; a backend without layers returns 0, 0.
	Compact(ctx context.Context) (before, after int, err error)
}

// Backend makes sandboxes on this machine.
type Backend interface {
	// Name is the backend's name, as -sandbox takes it: "microsandbox", "gvisor".
	Name() string
	// EnsureRuntime installs or checks what the backend needs. Call once at startup.
	EnsureRuntime(ctx context.Context) error
	// New starts a sandbox from cfg.Image. The caller must call Destroy when done.
	New(ctx context.Context, name string, cfg Config) (Sandbox, error)
	// Restore starts a sandbox called name from a snapshot. cfg.CPUs,
	// MemoryMiB, MaxLifetime and Secrets apply; cfg.Image is ignored.
	Restore(ctx context.Context, ref, name string, cfg Config) (Sandbox, error)
	// DeleteSnapshot removes a snapshot; one that no longer exists is no error.
	DeleteSnapshot(ctx context.Context, ref string) error
	// SnapshotGone reports whether a snapshot no longer exists on disk.
	SnapshotGone(ref string) bool
	// ListSnapshots returns every snapshot the backend has on this machine,
	// including ones forgerun didn't make.
	ListSnapshots(ctx context.Context) ([]SnapshotInfo, error)
	// RemoveLeftovers removes every sandbox whose name starts with prefix
	// (left by a run that crashed or was killed), and says how many.
	RemoveLeftovers(ctx context.Context, prefix string) (int, error)
}

// Default is the backend used when none is chosen.
var Default Backend = Microsandbox{}
