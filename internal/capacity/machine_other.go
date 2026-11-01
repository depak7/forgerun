//go:build !linux && !darwin

package capacity

import (
	"fmt"
	"runtime"
)

func totalMemMiB() (int, error) {
	return 0, fmt.Errorf("not supported on %s", runtime.GOOS)
}
