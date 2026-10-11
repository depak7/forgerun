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

func (c *Cursor) Name() string  { return "cursor" }
func (c *Cursor) Image() string { return "depak7/forgerun-agent-cursor:dev" }

func (c *Cursor) Secrets() []sandbox.Secret {
	return []sandbox.Secret{{
		EnvVar:     "CURSOR_API_KEY",
		Value:      c.apiKey,
		AllowHosts: []string{"*.cursor.sh", "*.cursor.com"},
	}}
}

func (c *Cursor) Run(ctx context.Context, sb *sandbox.Sandbox, dir, prompt string, logs io.Writer) error {
	if err := sb.WriteFile(ctx, "/root/.cursor/cli-config.json", []byte(cliConfig)); err != nil {
		return fmt.Errorf("writing cursor config: %w", err)
	}
	return sb.Stream(ctx, sandbox.Cmd{
		Name: "agent",
		Args: []string{"-p", prompt, "--force", "--trust", "--output-format", "text"},
		Dir:  dir,
		Env:  map[string]string{"HOME": "/root"}, // CURSOR_API_KEY comes from the VM as a placeholder
	}, logs)
}
