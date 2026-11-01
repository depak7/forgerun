package main

import (
	"cmp"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"

	"github.com/depak7/forgerun/internal/capacity"
	"github.com/depak7/forgerun/internal/sandbox"
)

// sandboxFlag registers -sandbox on fs.
func sandboxFlag(fs *flag.FlagSet) *string {
	return fs.String("sandbox", cmp.Or(os.Getenv("FORGERUN_SANDBOX"), "auto"),
		"where sessions run: microsandbox (microVMs; Apple Silicon or Linux with KVM), gvisor (Docker + gVisor; any Linux), or auto: microsandbox where it can run, else gvisor (env FORGERUN_SANDBOX)")
}

// sandboxName resolves "auto" to the backend this machine can run.
func sandboxName(name string) (string, error) {
	switch name {
	case "microsandbox", "gvisor":
		return name, nil
	case "", "auto":
		if microVMsUsable() {
			return "microsandbox", nil
		}
		if goruntime.GOOS == "linux" {
			return "gvisor", nil
		}
		return "", fmt.Errorf("this machine can't run microVMs (an Intel Mac?), and gVisor needs Linux")
	default:
		return "", fmt.Errorf("-sandbox must be auto, microsandbox or gvisor, not %q", name)
	}
}

// microVMsUsable reports whether microsandbox can run here: macOS on Apple
// Silicon, or Linux with a /dev/kvm this user can open.
func microVMsUsable() bool {
	switch goruntime.GOOS {
	case "darwin":
		return goruntime.GOARCH == "arm64"
	case "linux":
		f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
		if err != nil {
			return false
		}
		f.Close()
		return true
	}
	return false
}

// chooseBackend makes the backend called name (after sandboxName). The
// gVisor backend keeps its key proxy's CA certificate in stateDir.
func chooseBackend(name, stateDir string) (sandbox.Backend, error) {
	switch name {
	case "gvisor":
		return sandbox.NewGVisor(stateDir)
	default:
		return sandbox.Microsandbox{}, nil
	}
}

// defaultStateDir is where a command keeps its own state: ~/.forgerun/<sub>.
func defaultStateDir(sub string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "forgerun", sub)
	}
	return filepath.Join(home, ".forgerun", sub)
}

// reserveFlags registers -reserve-cpus and -reserve-mem on fs: what this
// machine keeps back from its sandboxes for the OS, Docker and forgerun.
func reserveFlags(fs *flag.FlagSet) func() capacity.Resources {
	cpus := fs.Int("reserve-cpus", 1, "CPUs this machine keeps for itself, never given to sandboxes")
	mem := byteSize(1 << 30)
	fs.Var(&mem, "reserve-mem", "memory this machine keeps for itself, never given to sandboxes, e.g. 1GiB or 512MiB")
	return func() capacity.Resources {
		return capacity.Resources{CPUs: *cpus, MemMiB: int(int64(mem) / (1 << 20))}
	}
}

// machineSize reads this machine's CPUs and memory. On failure it logs a
// warning and returns zero: capacity is then reported as unknown.
func machineSize(log *slog.Logger, reserve capacity.Resources) capacity.Resources {
	m, err := capacity.Machine()
	if err != nil {
		log.Warn("this machine's size is unknown, so it won't report its capacity", "err", err)
		return capacity.Resources{}
	}
	log.Info("capacity", "cpus", m.CPUs, "mem_mib", m.MemMiB, "reserved_cpus", reserve.CPUs, "reserved_mem_mib", reserve.MemMiB)
	return m
}
