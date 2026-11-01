package capacity

import (
	"bufio"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
)

// Machine returns this machine's CPUs and memory. Each OS has its own
// totalMemMiB (machine_linux.go, machine_darwin.go).
func Machine() (Resources, error) {
	mem, err := totalMemMiB()
	if err != nil {
		return Resources{}, fmt.Errorf("reading this machine's memory: %w", err)
	}
	return Resources{CPUs: runtime.NumCPU(), MemMiB: mem}, nil
}

// parseMeminfo reads MemTotal from Linux's /proc/meminfo, which gives it
// in KiB ("MemTotal:       16314660 kB").
func parseMeminfo(r io.Reader) (int, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || name != "MemTotal" {
			continue
		}
		kib, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(rest), " kB"))
		if err != nil {
			return 0, fmt.Errorf("MemTotal %q: %w", rest, err)
		}
		return kib / 1024, nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no MemTotal line")
}

// parseBytes reads a byte count, as macOS's sysctl hw.memsize prints it.
func parseBytes(s string) (int, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return int(n / (1 << 20)), nil
}
