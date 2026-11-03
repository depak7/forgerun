package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/depak7/forgerun/internal/sandbox"
)

// Pi runs pi (github.com/earendil-works/pi), a minimal coding agent that
// works with many model providers. Its model families here are
// "<provider>/<model>" in pi's own names, e.g.
// "openrouter/anthropic/claude-sonnet-5.5", and each provider has its own
// API key, which the proxy adds for that provider's host only. pi's
// subscription logins can't be used: they keep refreshing tokens in the VM.
type Pi struct{}

var (
	_ Agent     = (*Pi)(nil)
	_ Providers = (*Pi)(nil)
	_ Continuer = (*Pi)(nil)
	_ Replier   = (*Pi)(nil)
	_ Watched   = (*Pi)(nil)
)

// piProviders are the providers pi may use: the key pi reads for each, and
// the host that key may be sent to. Adding one is a line here and its
// models' rows in the models table.
var piProviders = []struct{ name, key, host string }{
	{"anthropic", "ANTHROPIC_API_KEY", "api.anthropic.com"},
	{"openai", "OPENAI_API_KEY", "api.openai.com"},
	{"openrouter", "OPENROUTER_API_KEY", "openrouter.ai"},
}

func (p *Pi) ID() string    { return "pi" }
func (p *Pi) Image() string { return "depak7/forgerun-agent-pi:dev" }
func (p *Pi) WritesReply()  {}

// Credential is "": pi's credential depends on the model (Providers).
func (p *Pi) Credential() string { return "" }

// Secrets is unused: a Providers agent's come from SecretFor.
func (p *Pi) Secrets(string) []sandbox.Secret { return nil }

// CredentialFor is the key of the model's provider, the part of its ID
// before the first slash.
func (p *Pi) CredentialFor(modelID string) string {
	provider, _, _ := strings.Cut(modelID, "/")
	for _, pr := range piProviders {
		if pr.name == provider {
			return pr.key
		}
	}
	return ""
}

func (p *Pi) Credentials() []string {
	var names []string
	for _, pr := range piProviders {
		names = append(names, pr.key)
	}
	slices.Sort(names)
	return names
}

// SecretFor: pi reads each key from its environment, where it's a
// placeholder; the proxy adds the key for its provider's host only.
func (p *Pi) SecretFor(name, value string) sandbox.Secret {
	s := sandbox.Secret{EnvVar: name, Value: value}
	for _, pr := range piProviders {
		if pr.key == name {
			s.AllowHosts = []string{pr.host}
		}
	}
	return s
}

// StatePaths: pi's whole home (sessions, settings, caches); it makes it
// again on its next run.
func (p *Pi) StatePaths() []string { return []string{"/root/.pi/agent"} }

// ActivityPaths: its sessions, which it appends to as it works. Its retries
// show as events in its output.
func (p *Pi) ActivityPaths() []string { return []string{"/root/.pi/agent/sessions"} }

func (p *Pi) Run(ctx context.Context, sb sandbox.Sandbox, req Request, logs io.Writer) error {
	ev := &piEvents{logs: logs}
	err := sb.Stream(ctx, piCmd(req.Dir, piArgs(req)), ev)
	ev.flush()
	if req.Reply != nil {
		io.WriteString(req.Reply, ev.reply)
	}
	return ev.result(err)
}

// Continue sends prompt to the latest session in dir with read-only tools,
// and returns the reply.
func (p *Pi) Continue(ctx context.Context, sb sandbox.Sandbox, dir, prompt string) (string, error) {
	out, err := sb.Run(ctx, piCmd(dir, piArgs(Request{Prompt: prompt, Continue: true, ReadOnly: true})))
	ev := &piEvents{logs: io.Discard}
	ev.Write([]byte(out))
	ev.flush()
	if err := ev.result(err); err != nil {
		return "", err
	}
	return ev.reply, nil
}

// piCmd runs pi with stdin closed and nothing fetched at startup: no update
// check, no telemetry, and no downloading fd or rg (the image has them).
func piCmd(dir string, args []string) sandbox.Cmd {
	return sandbox.Cmd{
		Name: "sh",
		Args: append([]string{"-c", `exec pi "$@" < /dev/null`, "pi"}, args...),
		Dir:  dir,
		Env: map[string]string{
			"HOME":         "/root",
			"PI_OFFLINE":   "1",
			"PI_TELEMETRY": "0",
		},
	}
}

// piReadOnlyTools are pi's tools that change nothing. pi has no plan mode:
// a read-only run gets only these.
const piReadOnlyTools = "read,grep,find,ls"

// piArgs builds the CLI arguments. The family is "<provider>/<model>"; the
// effort is pi's thinking level.
func piArgs(req Request) []string {
	args := []string{"--mode", "json"}
	if req.Continue {
		args = append(args, "--continue")
	}
	if req.ReadOnly {
		args = append(args, "--tools", piReadOnlyTools)
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

// piEvent is one line of `pi --mode json`: a finished message, a tool
// call, or a retried model call.
type piEvent struct {
	Type    string `json:"type"`
	Message struct {
		Role         string          `json:"role"`
		Content      json.RawMessage `json:"content"` // a string for a user message, parts otherwise
		StopReason   string          `json:"stopReason"`
		ErrorMessage string          `json:"errorMessage"`
	} `json:"message"`
	ToolName string         `json:"toolName"`
	Args     map[string]any `json:"args"`
	Result   struct {
		Content []piPart `json:"content"`
	} `json:"result"`
	IsError      bool   `json:"isError"`
	Attempt      int    `json:"attempt"`
	MaxAttempts  int    `json:"maxAttempts"`
	ErrorMessage string `json:"errorMessage"`
	Success      bool   `json:"success"`
	FinalError   string `json:"finalError"`
}

type piPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// piEvents turns pi's JSON events into a readable log, and keeps what the
// agent said last as the reply.
type piEvents struct {
	logs  io.Writer
	buf   []byte
	reply string
	err   string // why the run failed, from pi's own events
	// failed is the last model call's error, until a later call succeeds:
	// pi retries a failed call, and only the last attempt decides the run.
	failed string
}

func (e *piEvents) Write(p []byte) (int, error) {
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
func (e *piEvents) flush() {
	if len(e.buf) > 0 {
		e.line(e.buf)
		e.buf = nil
	}
}

func (e *piEvents) line(b []byte) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return
	}
	var ev piEvent
	if b[0] != '{' || json.Unmarshal(b, &ev) != nil || ev.Type == "" {
		fmt.Fprintf(e.logs, "%s\n", ansi.ReplaceAll(b, nil)) // not an event: pass it on
		return
	}
	switch ev.Type {
	case "message_end":
		e.message(ev)
	case "tool_execution_start":
		e.toolStart(ev)
	case "tool_execution_end":
		e.toolEnd(ev)
	case "auto_retry_start":
		fmt.Fprintf(e.logs, "  retrying (attempt %d of %d)\n", ev.Attempt, ev.MaxAttempts)
	case "auto_retry_end":
		if !ev.Success {
			e.fail(cmp.Or(ev.FinalError, "model calls kept failing"))
		}
	}
}

// message logs what the agent said, and a model call that failed.
func (e *piEvents) message(ev piEvent) {
	if ev.Message.Role != "assistant" {
		return
	}
	var parts []piPart
	json.Unmarshal(ev.Message.Content, &parts)
	var text []string
	for _, p := range parts {
		if t := strings.TrimSpace(p.Text); p.Type == "text" && t != "" {
			text = append(text, t)
		}
	}
	if t := strings.Join(text, "\n\n"); t != "" {
		fmt.Fprintf(e.logs, "%s\n\n", t)
		e.reply = t
	}
	if ev.Message.StopReason != "error" && ev.Message.StopReason != "aborted" {
		e.failed = ""
		return
	}
	e.failed = cmp.Or(ev.Message.ErrorMessage, "the model call "+ev.Message.StopReason)
	fmt.Fprintf(e.logs, "! model call failed: %s\n", e.failed)
}

// toolStart logs one tool call: "$ go test ./..." for a shell command,
// "→ edit main.go" for the rest.
func (e *piEvents) toolStart(ev piEvent) {
	if ev.ToolName == "bash" {
		fmt.Fprintf(e.logs, "$ %s\n", str(ev.Args["command"]))
		return
	}
	fmt.Fprintf(e.logs, "→ %s %s\n", ev.ToolName, cmp.Or(str(ev.Args["path"]), str(ev.Args["pattern"]), str(ev.Args["glob"])))
}

// toolEnd logs a shell command's output, and any tool's failure.
func (e *piEvents) toolEnd(ev piEvent) {
	var out []string
	for _, p := range ev.Result.Content {
		if p.Type == "text" {
			out = append(out, p.Text)
		}
	}
	text := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if len(text) > maxToolOutput {
		text = text[:maxToolOutput] + "\n… (output cut)"
	}
	switch {
	case ev.IsError:
		fmt.Fprintf(e.logs, "✗ %s: %s\n", ev.ToolName, cmp.Or(text, "failed"))
	case ev.ToolName == "bash" && text != "":
		fmt.Fprintf(e.logs, "%s\n", text)
	}
}

func (e *piEvents) fail(why string) {
	fmt.Fprintf(e.logs, "Error: %s\n", why)
	if e.err == "" {
		e.err = why
	}
}

// result is the run's error: pi's own reason explains a failed exit better
// than the exit code does, and fails the run even if the exit code didn't.
func (e *piEvents) result(err error) error {
	if e.err == "" && e.failed != "" {
		e.err = e.failed // its last model call failed, and pi gave up
	}
	if e.err == "" {
		return err
	}
	var exit *sandbox.ExitError
	if err == nil || errors.As(err, &exit) {
		return fmt.Errorf("pi: %s", e.err)
	}
	return fmt.Errorf("pi: %s: %w", e.err, err)
}
