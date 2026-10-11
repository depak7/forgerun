// Package agent knows how to drive a specific coding agent inside a sandbox.
package agent

import (
	"context"
	"io"

	"github.com/depak7/forgerun/internal/sandbox"
)

// Agent edits the repository at dir according to prompt, streaming its
// output to logs. It must not commit; the task pipeline collects changes
// from the working tree.
type Agent interface {
	Name() string
	Image() string // guest image that has this agent's CLI installed
	Run(ctx context.Context, sb *sandbox.Sandbox, dir, prompt string, logs io.Writer) error
}
