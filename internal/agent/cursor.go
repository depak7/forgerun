package agent

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/depak7/forgerun/internal/sandbox"
)

// cliConfig makes the agent connection use HTTP/1.1. The secret-swapping
// proxy can't negotiate HTTP/2, so without this the agent fails with
// "ALPN protocol failed to negotiate".
const cliConfig = `{"network":{"useHttp1ForAgent":true}}`

// Cursor drives the Cursor CLI ("agent") in headless mode.
type Cursor struct {
	apiKey string
}

var _ Agent = (*Cursor)(nil)

func NewCursor(apiKey string) (*Cursor, error) {
	if apiKey == "" {
		return nil, errors.New("cursor: API key is empty (set CURSOR_API_KEY)")
	}
	return &Cursor{apiKey: apiKey}, nil
}

func (c *Cursor) ID() string    { return "cursor" }
func (c *Cursor) Image() string { return "depak7/forgerun-agent-cursor:dev" }

func (c *Cursor) Secrets() []sandbox.Secret {
	return []sandbox.Secret{{
		EnvVar:     "CURSOR_API_KEY",
		Value:      c.apiKey,
		AllowHosts: []string{"*.cursor.sh", "*.cursor.com"},
	}}
}

// StatePaths: ~/.cursor isn't in the image (the CLI lives in ~/.local), so
// everything in it was written during the run: cli-config.json (rewritten
// by every Run), chats and project data.
func (c *Cursor) StatePaths() []string { return []string{"/root/.cursor"} }

func (c *Cursor) Run(ctx context.Context, sb *sandbox.Sandbox, req Request, logs io.Writer) error {
	if err := sb.WriteFile(ctx, "/root/.cursor/cli-config.json", []byte(cliConfig)); err != nil {
		return fmt.Errorf("writing cursor config: %w", err)
	}
	return sb.Stream(ctx, sandbox.Cmd{
		Name: "agent",
		Args: cursorArgs(req),
		Dir:  req.Dir,
		Env:  map[string]string{"HOME": "/root"}, // CURSOR_API_KEY comes from the VM as a placeholder
	}, logs)
}

// cursorArgs builds the CLI arguments. "auto" is Cursor's default, so it
// is left implicit rather than passed as --model auto.
func cursorArgs(req Request) []string {
	args := []string{"-p", req.Prompt, "--force", "--trust", "--output-format", "text"}
	if req.ModelID != "" && req.ModelID != "auto" {
		args = append(args, "--model", req.ModelID)
	}
	return args
}
