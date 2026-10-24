package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/depak7/forgerun/internal/sandbox"
)

// OpenCode runs the OpenCode CLI on OpenCode's own hosted models
// (opencode/<id>). Free models need no key; paid ones bill the workspace
// that OPENCODE_API_KEY belongs to.
type OpenCode struct{}

var (
	_ Agent     = (*OpenCode)(nil)
	_ Continuer = (*OpenCode)(nil)
	_ Replier   = (*OpenCode)(nil)
)

func (o *OpenCode) ID() string         { return "opencode" }
func (o *OpenCode) Image() string      { return "depak7/forgerun-agent-opencode:dev" }
func (o *OpenCode) Credential() string { return "OPENCODE_API_KEY" }
func (o *OpenCode) WritesReply()       {}

// Secrets: the CLI reads OPENCODE_API_KEY from its environment, where it's
// a placeholder; the proxy adds the key for opencode.ai only.
func (o *OpenCode) Secrets(key string) []sandbox.Secret {
	return []sandbox.Secret{{
		EnvVar:     o.Credential(),
		Value:      key,
		AllowHosts: []string{"opencode.ai"},
	}}
}

// StatePaths: sessions (opencode.db), its own undo snapshots of the repo,
// and logs. Its config and cache dirs hold no chat and come from the
// image, so they stay.
func (o *OpenCode) StatePaths() []string {
	return []string{"/root/.local/share/opencode", "/root/.local/state/opencode"}
}

func (o *OpenCode) Run(ctx context.Context, sb sandbox.Sandbox, req Request, logs io.Writer) error {
	ev := &opencodeEvents{logs: logs}
	err := sb.Stream(ctx, opencodeCmd(req.Dir, opencodeArgs(req)), ev)
	ev.flush()
	if req.Reply != nil {
		io.WriteString(req.Reply, ev.reply())
	}
	return ev.result(err)
}

// Continue sends prompt to the latest session in dir with the read-only
// plan agent, and returns the reply.
func (o *OpenCode) Continue(ctx context.Context, sb sandbox.Sandbox, dir, prompt string) (string, error) {
	out, err := sb.Run(ctx, opencodeCmd(dir, opencodeArgs(Request{Prompt: prompt, Continue: true, ReadOnly: true})))
	ev := &opencodeEvents{logs: io.Discard}
	ev.Write([]byte(out))
	ev.flush()
	if err := ev.result(err); err != nil {
		return "", err
	}
	return ev.reply(), nil
}

// opencodeCmd runs the CLI with stdin closed, never updating itself or
// downloading language servers mid-task (that would also bloat snapshots).
func opencodeCmd(dir string, args []string) sandbox.Cmd {
	return sandbox.Cmd{
		Name: "sh",
		Args: append([]string{"-c", `exec opencode "$@" < /dev/null`, "opencode"}, args...),
		Dir:  dir,
		Env: map[string]string{
			"HOME":                          "/root",
			"OPENCODE_DISABLE_AUTOUPDATE":   "1",
			"OPENCODE_DISABLE_LSP_DOWNLOAD": "1",
		},
	}
}

// opencodeArgs builds the CLI arguments. --auto approves the tools an agent
// is allowed: everything for the default build agent, and no edits for the
// plan agent, which reads and proposes. Model IDs are OpenCode's own
// ("big-pickle"), so the provider is always "opencode".
func opencodeArgs(req Request) []string {
	args := []string{"run", "--format", "json", "--auto"}
	if req.ReadOnly {
		args = append(args, "--agent", "plan")
	}
	if req.Continue {
		args = append(args, "--continue")
	}
	if req.ModelID != "" && req.ModelID != "auto" {
		args = append(args, "--model", "opencode/"+req.ModelID)
	}
	if req.Effort != "" {
		args = append(args, "--variant", req.Effort)
	}
	return append(args, "--", req.Prompt)
}

// opencodeEvent is one line of `opencode run --format json`: what the
// agent said (text), a tool it ran (tool_use), or a failure (error).
type opencodeEvent struct {
	Type string `json:"type"`
	Part struct {
		Text  string `json:"text"`
		Tool  string `json:"tool"`
		State struct {
			Status string         `json:"status"` // "completed" or "error"
			Title  string         `json:"title"`
			Input  map[string]any `json:"input"`
			Output string         `json:"output"`
			Error  string         `json:"error"`
		} `json:"state"`
	} `json:"part"`
	Error struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// maxToolOutput is how much of a shell command's output reaches the log.
const maxToolOutput = 2000

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// opencodeEvents turns the CLI's JSON events into a readable log, and keeps
// the agent's last words (what it said after its last tool) as the reply.
type opencodeEvents struct {
	logs io.Writer
	buf  []byte
	last []string // text since the last tool call
	err  string   // the first error event's message
}

func (e *opencodeEvents) Write(p []byte) (int, error) {
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
func (e *opencodeEvents) flush() {
	if len(e.buf) > 0 {
		e.line(e.buf)
		e.buf = nil
	}
}

func (e *opencodeEvents) line(b []byte) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return
	}
	var ev opencodeEvent
	if b[0] != '{' || json.Unmarshal(b, &ev) != nil || ev.Type == "" {
		fmt.Fprintf(e.logs, "%s\n", ansi.ReplaceAll(b, nil)) // not an event: pass it on
		return
	}
	switch ev.Type {
	case "text":
		if t := strings.TrimSpace(ev.Part.Text); t != "" {
			fmt.Fprintf(e.logs, "%s\n\n", t)
			e.last = append(e.last, t)
		}
	case "tool_use":
		e.last = nil
		e.tool(ev)
	case "error":
		msg := cmp.Or(ev.Error.Data.Message, ev.Error.Name, "unknown error")
		fmt.Fprintf(e.logs, "Error: %s\n", msg)
		if e.err == "" {
			e.err = msg
		}
	}
}

// tool logs one tool call: shell commands with their output, other tools
// as one line, e.g. "→ edit math.js".
func (e *opencodeEvents) tool(ev opencodeEvent) {
	s := ev.Part.State
	what := cmp.Or(s.Title, str(s.Input["filePath"]), str(s.Input["pattern"]), str(s.Input["command"]))
	if s.Status == "error" {
		fmt.Fprintf(e.logs, "✗ %s %s: %s\n", ev.Part.Tool, what, cmp.Or(s.Error, "failed"))
		return
	}
	if ev.Part.Tool != "bash" {
		fmt.Fprintf(e.logs, "→ %s %s\n", ev.Part.Tool, what)
		return
	}
	fmt.Fprintf(e.logs, "$ %s\n", cmp.Or(str(s.Input["command"]), what))
	if out := strings.TrimRight(s.Output, "\n"); out != "" {
		if len(out) > maxToolOutput {
			out = out[:maxToolOutput] + "\n… (output cut)"
		}
		fmt.Fprintf(e.logs, "%s\n", out)
	}
}

// reply is what the agent said after its last tool call.
func (e *opencodeEvents) reply() string { return strings.Join(e.last, "\n\n") }

// result is the run's error: an error event explains a failed exit better
// than the exit code does, and fails the run even if the exit code didn't.
func (e *opencodeEvents) result(err error) error {
	if e.err == "" {
		return err
	}
	var exit *sandbox.ExitError
	if err == nil || errors.As(err, &exit) {
		return fmt.Errorf("opencode: %s", e.err)
	}
	return fmt.Errorf("opencode: %s: %w", e.err, err)
}

// str is v as a string, or "" if it isn't one.
func str(v any) string {
	s, _ := v.(string)
	return s
}
