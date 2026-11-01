package capacity

import "os/exec"

func totalMemMiB() (int, error) {
	// The sysctl program rather than syscall.Sysctl, which returns the raw
	// number as a string and drops a trailing zero byte from it.
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, err
	}
	return parseBytes(string(out))
}
