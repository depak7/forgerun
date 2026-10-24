package main

import (
	"cmp"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"

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
