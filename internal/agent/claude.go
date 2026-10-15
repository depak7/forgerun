package agent

import (
	"context"
	"io"
	"strings"

	"github.com/depak7/forgerun/internal/sandbox"
)

// ClaudeCode runs Anthropic's Claude Code CLI, authenticated with a
// subscription token from `claude setup-token`.
type ClaudeCode struct{}

var (
	_ Agent     = (*ClaudeCode)(nil)
	_ Continuer = (*ClaudeCode)(nil)
)

func (c *ClaudeCode) ID() string         { return "claude-code" }
func (c *ClaudeCode) Image() string      { return "depak7/forgerun-agent-claude:dev" }
func (c *ClaudeCode) Credential() string { return "CLAUDE_CODE_OAUTH_TOKEN" }

// Secrets: the token reaches the VM only as a placeholder; the proxy adds
// it for api.anthropic.com.
func (c *ClaudeCode) Secrets(token string) []sandbox.Secret {
	return []sandbox.Secret{{
		EnvVar:     c.Credential(),
		Value:      token,
		AllowHosts: []string{"api.anthropic.com"},
	}}
}

// StatePaths: the CLI's config, transcripts and per-project auto-memory,
// all written during runs. Wiping them keeps one task's memories out of
// the next.
func (c *ClaudeCode) StatePaths() []string { return []string{"/root/.claude", "/root/.claude.json"} }

func (c *ClaudeCode) Run(ctx context.Context, sb *sandbox.Sandbox, req Request, logs io.Writer) error {
	return sb.Stream(ctx, claudeCmd(req.Dir, claudeArgs(req)), logs)
}

// Continue sends prompt to the latest conversation in dir, in read-only
// plan mode, and returns the reply.
func (c *ClaudeCode) Continue(ctx context.Context, sb *sandbox.Sandbox, dir, prompt string) (string, error) {
	args := []string{"-p", prompt, "--continue", "--permission-mode", "plan", "--output-format", "text"}
	out, err := sb.Run(ctx, claudeCmd(dir, args))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// claudeCmd runs the CLI with stdin closed: with a pipe it waits 3s for
// input first. IS_SANDBOX=1 lets it skip permission prompts as root, which
// it otherwise refuses; the VM is the sandbox.
func claudeCmd(dir string, args []string) sandbox.Cmd {
	return sandbox.Cmd{
		Name: "sh",
		Args: append([]string{"-c", `exec claude "$@" < /dev/null`, "claude"}, args...),
		Dir:  dir,
		Env:  map[string]string{"HOME": "/root", "IS_SANDBOX": "1"},
	}
}

// claudeArgs builds the CLI arguments for a task. The model's family is a
// CLI alias (sonnet, opus, …) and its effort a separate flag; with neither,
// the CLI uses its own defaults.
func claudeArgs(req Request) []string {
	args := []string{"-p", req.Prompt, "--permission-mode", "bypassPermissions", "--output-format", "text"}
	if req.Family != "" {
		args = append(args, "--model", req.Family)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	return args
}
