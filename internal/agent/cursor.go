package agent

import (
	"context"
	"errors"
	"io"

	"github.com/depak7/forgerun/internal/sandbox"
)

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

func (c *Cursor) Run(ctx context.Context, sb *sandbox.Sandbox, dir, prompt string, logs io.Writer) error {
	return sb.Stream(ctx, sandbox.Cmd{
		Name: "agent",
		Args: []string{"-p", prompt, "--force", "--trust", "--output-format", "text"},
		Dir:  dir,
		Env: map[string]string{
			"HOME":           "/root",
			"CURSOR_API_KEY": c.apiKey, // M2 replaces this with WithSecrets
		},
	}, logs)
}
