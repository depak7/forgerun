package capacity

import "os"

func totalMemMiB() (int, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return parseMeminfo(f)
}
