package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/depak7/forgerun/internal/sandbox"
)

// Codex runs OpenAI's Codex CLI (codex exec) on OpenAI's own models. Its
// model families are the CLI's slugs (gpt-6.1-sol, …) and effort is its
// model_reasoning_effort level. Authenticated with an OpenAI API key; the
// ChatGPT sign-in can't be used: it keeps refreshing tokens in the VM.
type Codex struct{}

var (
	_ Agent     = (*Codex)(nil)
	_ Continuer = (*Codex)(nil)
	_ Replier   = (*Codex)(nil)
)

func (c *Codex) ID() string         { return "codex" }
func (c *Codex) Image() string      { return "depak7/forgerun-agent-codex:dev" }
func (c *Codex) Credential() string { return "OPENAI_API_KEY" }
func (c *Codex) WritesReply()       {}

// Secrets: the CLI reads OPENAI_API_KEY from its environment (it beats an
// auth.json), where it's a placeholder; the proxy adds the key for
// api.openai.com only.
func (c *Codex) Secrets(key string) []sandbox.Secret {
	return []sandbox.Secret{{
		EnvVar:     c.Credential(),
		Value:      key,
		AllowHosts: []string{"api.openai.com"},
	}}
}

// StatePaths: the CLI's whole home. It holds the session rollouts and
// thread database a follow-up continues, the config the CLI writes on its
// first run (including the repo's trust entry), and any skills a run
// installed: none of it may leak into another task.
func (c *Codex) StatePaths() []string { return []string{"/root/.codex"} }

func (c *Codex) Run(ctx context.Context, sb sandbox.Sandbox, req Request, logs io.Writer) error {
	ev := &codexEvents{logs: logs}
	err := sb.Stream(ctx, codexCmd(req.Dir, codexArgs(req)), ev)
	ev.flush()
	if req.Reply != nil {
		io.WriteString(req.Reply, ev.reply)
	}
	return ev.result(err)
}

// Continue sends prompt to the session the last Run started, read-only, and
// returns the reply.
func (c *Codex) Continue(ctx context.Context, sb sandbox.Sandbox, dir, prompt string) (string, error) {
	out, err := sb.Run(ctx, codexCmd(dir, codexArgs(Request{Prompt: prompt, Continue: true, ReadOnly: true})))
	ev := &codexEvents{logs: io.Discard}
	ev.Write([]byte(out))
	ev.flush()
	if err := ev.result(err); err != nil {
		return "", err
	}
	return ev.reply, nil
}

// codexCmd runs the CLI with stdin closed: with a pipe it reads more
// instructions from it.
func codexCmd(dir string, args []string) sandbox.Cmd {
	return sandbox.Cmd{
		Name: "sh",
		Args: append([]string{"-c", `exec codex "$@" < /dev/null`, "codex"}, args...),
		Dir:  dir,
		Env:  map[string]string{"HOME": "/root"},
	}
}

// codexArgs builds the CLI arguments. exec never asks for approval; the
// sandbox mode is a config override rather than --sandbox because
// `exec resume`, which continues the last session, has no such flag. The
// family is the CLI's model slug and the effort a reasoning level; with
// neither, the CLI uses its own defaults. "--": a prompt that starts with
// "-" is the prompt, not an option.
func codexArgs(req Request) []string {
	mode := "workspace-write"
	if req.ReadOnly {
		mode = "read-only" // reads and proposes; edits nothing
	}
	var args []string
	if req.Continue {
		args = []string{"exec", "--json", "--color", "never", "resume", "--last"}
	} else {
		args = []string{"exec", "--json", "--color", "never"}
	}
	args = append(args,
		"-c", `approval_policy="never"`,
		"-c", `sandbox_mode="`+mode+`"`,
	)
	if req.Family != "" {
		args = append(args, "-m", req.Family)
	}
	if req.Effort != "" {
		args = append(args, "-c", `model_reasoning_effort="`+req.Effort+`"`)
	}
	return append(args, "--", req.Prompt)
}

// codexEvent is one line of `codex exec --json`: a lifecycle event, one
// item's start or completion, or an error the CLI is working around
// (a reconnect) or failing on (turn.failed).
type codexEvent struct {
	Type  string `json:"type"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"` // turn.failed
	Item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`              // agent_message
		Command string `json:"command"`           // command_execution
		Status  string `json:"status"`            // command_execution, others
		Output  string `json:"aggregated_output"` // command_execution
		Exit    *int   `json:"exit_code"`         // command_execution
		Message string `json:"message"`           // error
		Changes []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"` // file_change
		Server string `json:"server"` // mcp_tool_call
		Tool   string `json:"tool"`   // mcp_tool_call
	} `json:"item"`
	Message string `json:"message"` // error
}

// codexEvents turns the CLI's JSON events into a readable log, and keeps
// the agent's last message as the reply.
type codexEvents struct {
	logs  io.Writer
	buf   []byte
	reply string
	err   string // why the turn failed, from turn.failed
	// failed is the last error event, until the turn completes: the CLI
	// retries a dropped connection, and only the turn's end decides the run.
	failed string
}

func (e *codexEvents) Write(p []byte) (int, error) {
	e.buf = append(e.buf, p...)
	for {
		i := bytes.IndexByte(e.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		e.line(e.buf[:i])
		e.buf = e.buf[i+1:]
	}
}

// flush handles a last line that had no newline.
func (e *codexEvents) flush() {
	if len(e.buf) > 0 {
		e.line(e.buf)
		e.buf = nil
	}
}

func (e *codexEvents) line(b []byte) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return
	}
	var ev codexEvent
	if b[0] != '{' || json.Unmarshal(b, &ev) != nil || ev.Type == "" {
		fmt.Fprintf(e.logs, "%s\n", ansi.ReplaceAll(b, nil)) // not an event: pass it on
		return
	}
	switch ev.Type {
	case "item.started":
		if ev.Item.Type == "command_execution" {
			fmt.Fprintf(e.logs, "$ %s\n", ev.Item.Command)
		}
	case "item.completed":
		e.item(ev)
	case "error":
		// Recoverable while the run goes on, e.g. "Reconnecting... 2/5".
		e.failed = cmp.Or(ev.Message, "unknown error")
		fmt.Fprintf(e.logs, "! %s\n", e.failed)
	case "turn.completed":
		e.failed = ""
	case "turn.failed":
		e.fail(cmp.Or(ev.Error.Message, ev.Message, "the turn failed"))
	}
}

// item logs a completed item: the agent's words, a command's output, a file
// change, a tool call, or an error the CLI attached to the turn.
func (e *codexEvents) item(ev codexEvent) {
	it := ev.Item
	switch it.Type {
	case "agent_message":
		if t := strings.TrimSpace(it.Text); t != "" {
			fmt.Fprintf(e.logs, "%s\n\n", t)
			e.reply = t
		}
	case "command_execution":
		out := strings.TrimRight(it.Output, "\n")
		if len(out) > maxToolOutput {
			out = out[:maxToolOutput] + "\n… (output cut)"
		}
		if it.Exit != nil && *it.Exit != 0 {
			fmt.Fprintf(e.logs, "✗ exit %d\n%s\n", *it.Exit, out)
		} else if out != "" {
			fmt.Fprintf(e.logs, "%s\n", out)
		}
	case "file_change":
		var paths []string
		for _, ch := range it.Changes {
			paths = append(paths, ch.Path)
		}
		fmt.Fprintf(e.logs, "→ edit %s\n", cmp.Or(strings.Join(paths, ", "), "files"))
	case "mcp_tool_call":
		fmt.Fprintf(e.logs, "→ %s %s\n", cmp.Or(it.Server+"/"+it.Tool, it.Tool, "mcp"), it.Status)
	case "error":
		fmt.Fprintf(e.logs, "Error: %s\n", cmp.Or(it.Message, "unknown error"))
	}
}

func (e *codexEvents) fail(why string) {
	fmt.Fprintf(e.logs, "Error: %s\n", why)
	if e.err == "" {
		e.err = why
	}
}

// result is the run's error: turn.failed's reason explains a failed exit
// better than the exit code does, and fails the run even if the exit code
// didn't. A run that died mid-reconnect is explained by its last error
// event.
func (e *codexEvents) result(err error) error {
	if e.err == "" && err != nil {
		var exit *sandbox.ExitError
		if e.failed != "" && errors.As(err, &exit) {
			return fmt.Errorf("codex: %s: %w", e.failed, err)
		}
		return err
	}
	if e.err == "" {
		return nil
	}
	var exit *sandbox.ExitError
	if err == nil || errors.As(err, &exit) {
		return fmt.Errorf("codex: %s", e.err)
	}
	return fmt.Errorf("codex: %s: %w", e.err, err)
}
