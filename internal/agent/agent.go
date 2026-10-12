// Package agent knows how to drive a specific coding agent inside a sandbox.
package agent

import (
	"context"
	"io"

	"github.com/depak7/forgerun/internal/sandbox"
)

// Request is one agent run. Adding a field here doesn't change any
// implementation's signature.
type Request struct {
	Dir     string // repository checkout inside the VM
	Prompt  string
	ModelID string // exact agent model id; "" or "auto" = the agent's own choice
}

// Agent edits the repository at req.Dir according to req.Prompt, streaming its
// output to logs. It must not commit; the task pipeline collects changes
// from the working tree.
type Agent interface {
	ID() string                // matches agents.id in the database, e.g. "cursor"
	Image() string             // guest image that has this agent's CLI installed
	Secrets() []sandbox.Secret // credentials to inject via the proxy, never as plain env
	Run(ctx context.Context, sb *sandbox.Sandbox, req Request, logs io.Writer) error
}
