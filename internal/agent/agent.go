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
	// The chosen model's family and effort, from the models table, for agents
	// that take them as separate options (Claude Code: --model opus --effort
	// high). Agents whose IDs already encode them (Cursor) ignore these.
	Family string
	Effort string
	// Continue asks the agent to carry on the conversation an earlier Run
	// started in this VM, so it remembers what was said and done. Only
	// agents that are Continuers honour it; the caller checks that.
	Continue bool
	// ReadOnly asks the agent to look and plan without editing files, in
	// its own read-only mode if it has one. forgerun discards any changes
	// a read-only run makes anyway.
	ReadOnly bool
	// Reply receives what the agent finally said, for agents that are
	// Repliers; nil = not wanted. Others leave it alone, and their reply is
	// the end of their output.
	Reply io.Writer
}

// Agent edits the repository at req.Dir according to req.Prompt, streaming its
// output to logs. It must not commit; the task pipeline collects changes
// from the working tree.
type Agent interface {
	ID() string    // matches agents.id in the database, e.g. "cursor"
	Image() string // guest image that has this agent's CLI installed
	// Credential names the agent's credential, e.g. "CURSOR_API_KEY": the
	// variable it's configured in on the host and the placeholder it
	// appears as inside the VM. "" if the agent needs none.
	Credential() string
	// Secrets turns the credential's value into secrets for the proxy to
	// inject; the value itself never enters the VM.
	Secrets(credential string) []sandbox.Secret
	// StatePaths are paths in the VM where the agent keeps per-run state
	// (chat history, sessions, config written by Run). They're deleted
	// before a VM is snapshotted, so the next task starts clean.
	StatePaths() []string
	Run(ctx context.Context, sb sandbox.Sandbox, req Request, logs io.Writer) error
}

// Continuer is an optional Agent capability: carrying on the conversation
// the last Run started in the same VM. A Continuer honours Request.Continue
// in Run, and Continue sends one read-only prompt and returns the reply;
// forgerun uses that for summaries, which only the agent's own chat can
// give (what the user asked for as well as what was done). Continue must
// not change files.
//
// Check for it with a type assertion: if c, ok := a.(Continuer); ok { ... }
type Continuer interface {
	Continue(ctx context.Context, sb sandbox.Sandbox, dir, prompt string) (string, error)
}

// Watched is an optional Agent capability: the paths in the VM whose changes
// show the agent is still working, for the stuck-agent watchdog (besides
// the repo and its output). Without it, they're its StatePaths: right for
// an agent that writes little output but keeps a transcript (Claude Code),
// wrong for one that keeps writing its own logs while stuck retrying.
type Watched interface {
	ActivityPaths() []string
}

// ActivityPaths are the paths that show a's work, for the watchdog.
func ActivityPaths(a Agent) []string {
	if w, ok := a.(Watched); ok {
		return w.ActivityPaths()
	}
	return a.StatePaths()
}

// Replier is an optional Agent capability, for agents whose output mixes
// what they say with the tools they run: Run writes the agent's final
// words to Request.Reply when it's set, as well as everything to logs.
type Replier interface {
	WritesReply()
}
