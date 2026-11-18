package agent

import (
	"context"
	"io"
	"strings"

	"github.com/depak7/forgerun/internal/sandbox"
)

// Prime runs Prime Intellect's prime-agent
// (github.com/PrimeIntellect-ai/prime-agent), an RLM coding harness with a
// persistent Python REPL. Model families are "<provider>/<model>" in
// prime-agent's own names; each provider has its own API key, which the
// proxy adds for that provider's host only. Interactive /login
// subscriptions can't be used: they refresh tokens inside the VM.
type Prime struct{}

var (
	_ Agent     = (*Prime)(nil)
	_ Providers = (*Prime)(nil)
	_ Continuer = (*Prime)(nil)
	_ Replier   = (*Prime)(nil)
	_ Watched   = (*Prime)(nil)
)

// primeKeys are the providers prime supports (see providers).
var primeKeys = providerKeys{"anthropic", "openai", "openrouter", "google", "xai", "prime-inference"}

func (p *Prime) ID() string    { return "prime" }
func (p *Prime) Image() string { return "depak7/forgerun-agent-prime:dev" }
func (p *Prime) WritesReply()  {}

// Credential is "": prime's credential depends on the model (Providers).
func (p *Prime) Credential() string { return "" }

// Secrets is unused: a Providers agent's come from SecretFor.
func (p *Prime) Secrets(string) []sandbox.Secret { return nil }

func (p *Prime) CredentialFor(modelID string) string { return primeKeys.CredentialFor(modelID) }
func (p *Prime) Credentials() []string               { return primeKeys.Credentials() }
func (p *Prime) SecretFor(name, value string) sandbox.Secret {
	return primeKeys.SecretFor(name, value)
}

// StatePaths: ~/.prime holds sessions, auth, and the kernel venv; wiping
// it keeps one task's memories out of the next snapshot.
func (p *Prime) StatePaths() []string { return []string{"/root/.prime"} }

// ActivityPaths: sessions, which it appends to as it works. Retries show
// as events in its output.
func (p *Prime) ActivityPaths() []string {
	return []string{"/root/.prime/agent/sessions"}
}

func (p *Prime) Run(ctx context.Context, sb sandbox.Sandbox, req Request, logs io.Writer) error {
	return runEvents(ctx, sb, "prime", primeCmd(req.Dir, primeArgs(req)), req.Reply, logs)
}

// Continue sends prompt to the latest session in dir with a read-only
// system addendum, and returns the reply.
func (p *Prime) Continue(ctx context.Context, sb sandbox.Sandbox, dir, prompt string) (string, error) {
	return continueEvents(ctx, sb, "prime", primeCmd(dir, primeArgs(Request{Prompt: prompt, Continue: true, ReadOnly: true})))
}

// primeCmd runs prime-agent with stdin closed, its REPL on the image's
// prebuilt kernel (so it never bootstraps one into the session), and no
// update checks or analytics during a task.
func primeCmd(dir string, args []string) sandbox.Cmd {
	return sandbox.Cmd{
		Name: "sh",
		Args: append([]string{"-c", `exec prime-agent "$@" < /dev/null`, "prime-agent"}, args...),
		Dir:  dir,
		Env: map[string]string{
			"HOME":                      "/root",
			"PI_OFFLINE":                "1",
			"PRIME_AGENT_TELEMETRY":     "0",
			"DO_NOT_TRACK":              "1",
			"PRIME_AGENT_KERNEL_PYTHON": "/opt/prime-kernel/bin/python",
		},
	}
}

// primeReadOnlyPrompt is appended on plan / summary turns. Rust prime-agent
// 0.10.3 has no --tools allowlist, so this is only a request; the runner
// resets the repository to its starting commit after every read-only run.
const primeReadOnlyPrompt = "Do not create, edit, delete, or overwrite any files. Do not run commands that change the repository. Read and answer only."

// primeArgs builds the CLI arguments. The family is "<provider>/<model>";
// the effort is prime-agent's thinking level.
func primeArgs(req Request) []string {
	args := []string{"--mode", "json", "--offline", "--no-skills"}
	if req.Continue {
		args = append(args, "--continue")
	}
	if req.ReadOnly {
		args = append(args, "--append-system-prompt", primeReadOnlyPrompt)
	}
	if provider, model, ok := strings.Cut(req.Family, "/"); ok {
		args = append(args, "--provider", provider, "--model", model)
	}
	if req.Effort != "" {
		args = append(args, "--thinking", req.Effort)
	}
	// "--": a prompt that starts with "-" is the prompt, not an option.
	return append(args, "--", req.Prompt)
}
