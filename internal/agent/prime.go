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

// primeProviders are the providers prime-agent may use: the key it reads
// for each, and the host that key may be sent to. Adding one is a line
// here and its models' rows in the models table.
var primeProviders = []struct{ name, key, host string }{
	{"anthropic", "ANTHROPIC_API_KEY", "api.anthropic.com"},
	{"openai", "OPENAI_API_KEY", "api.openai.com"},
	{"openrouter", "OPENROUTER_API_KEY", "openrouter.ai"},
	{"google", "GEMINI_API_KEY", "generativelanguage.googleapis.com"},
	{"xai", "XAI_API_KEY", "api.x.ai"},
	{"prime-inference", "PRIME_API_KEY", "api.pinference.ai"},
}

func (p *Prime) ID() string    { return "prime" }
func (p *Prime) Image() string { return "depak7/forgerun-agent-prime:dev" }
func (p *Prime) WritesReply()  {}

// Credential is "": prime's credential depends on the model (Providers).
func (p *Prime) Credential() string { return "" }

// Secrets is unused: a Providers agent's come from SecretFor.
func (p *Prime) Secrets(string) []sandbox.Secret { return nil }

// CredentialFor is the key of the model's provider, the part of its ID
// before the first slash.
func (p *Prime) CredentialFor(modelID string) string {
	provider, _, _ := strings.Cut(modelID, "/")
	for _, pr := range primeProviders {
		if pr.name == provider {
			return pr.key
		}
	}
	return ""
}

func (p *Prime) Credentials() []string {
	var names []string
	for _, pr := range primeProviders {
		names = append(names, pr.key)
	}
	slices.Sort(names)
	return names
}

// SecretFor: prime-agent reads each key from its environment, where it's a
// placeholder; the proxy adds the key for its provider's host only.
func (p *Prime) SecretFor(name, value string) sandbox.Secret {
	s := sandbox.Secret{EnvVar: name, Value: value}
	for _, pr := range primeProviders {
		if pr.key == name {
			s.AllowHosts = []string{pr.host}
		}
	}
	return s
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
	ev := &primeEvents{logs: logs}
	err := sb.Stream(ctx, primeCmd(req.Dir, primeArgs(req)), ev)
	ev.flush()
	if req.Reply != nil {
		io.WriteString(req.Reply, ev.reply)
	}
	return ev.result(err)
}

// Continue sends prompt to the latest session in dir with a read-only
// system addendum, and returns the reply.
func (p *Prime) Continue(ctx context.Context, sb sandbox.Sandbox, dir, prompt string) (string, error) {
	out, err := sb.Run(ctx, primeCmd(dir, primeArgs(Request{Prompt: prompt, Continue: true, ReadOnly: true})))
	ev := &primeEvents{logs: io.Discard}
	ev.Write([]byte(out))
	ev.flush()
	if err := ev.result(err); err != nil {
		return "", err
	}
	return ev.reply, nil
}

// primeCmd runs prime-agent with stdin closed. The image's
// PRIME_AGENT_KERNEL_PYTHON supplies the REPL; PI_OFFLINE / telemetry off
// stop update checks and analytics during a task.
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
// 0.10.3 has no --tools allowlist; forgerun also discards any file changes
// on read-only runs.
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

// primeEvent is one line of `prime-agent --mode json`: a finished message,
// a tool call, or a retried model call (same shape as pi).
type primeEvent struct {
	Type    string `json:"type"`
	Message struct {
		Role         string          `json:"role"`
		Content      json.RawMessage `json:"content"`
		StopReason   string          `json:"stopReason"`
		ErrorMessage string          `json:"errorMessage"`
	} `json:"message"`
	ToolName string         `json:"toolName"`
	Args     map[string]any `json:"args"`
	Result   struct {
		Content []primePart `json:"content"`
	} `json:"result"`
	IsError      bool   `json:"isError"`
	Attempt      int    `json:"attempt"`
	MaxAttempts  int    `json:"maxAttempts"`
	ErrorMessage string `json:"errorMessage"`
	Success      bool   `json:"success"`
	FinalError   string `json:"finalError"`
}

type primePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// primeEvents turns prime-agent's JSON events into a readable log, and
// keeps what the agent said last as the reply.
type primeEvents struct {
	logs   io.Writer
	buf    []byte
	reply  string
	err    string // why the run failed, from its own events
	failed string // last model-call error until a later call succeeds
}

func (e *primeEvents) Write(p []byte) (int, error) {
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

func (e *primeEvents) flush() {
	if len(e.buf) > 0 {
		e.line(e.buf)
		e.buf = nil
	}
}

func (e *primeEvents) line(b []byte) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return
	}
	var ev primeEvent
	if b[0] != '{' || json.Unmarshal(b, &ev) != nil || ev.Type == "" {
		fmt.Fprintf(e.logs, "%s\n", ansi.ReplaceAll(b, nil))
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

func (e *primeEvents) message(ev primeEvent) {
	if ev.Message.Role != "assistant" {
		return
	}
	var parts []primePart
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

func (e *primeEvents) toolStart(ev primeEvent) {
	switch ev.ToolName {
	case "bash":
		fmt.Fprintf(e.logs, "$ %s\n", str(ev.Args["command"]))
	case "ipython", "python_repl":
		code := str(ev.Args["code"])
		if len(code) > 120 {
			code = code[:120] + "…"
		}
		fmt.Fprintf(e.logs, "→ %s %s\n", ev.ToolName, strings.ReplaceAll(code, "\n", " "))
	default:
		fmt.Fprintf(e.logs, "→ %s %s\n", ev.ToolName, cmp.Or(str(ev.Args["path"]), str(ev.Args["pattern"]), str(ev.Args["glob"])))
	}
}

func (e *primeEvents) toolEnd(ev primeEvent) {
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
	case (ev.ToolName == "bash" || ev.ToolName == "ipython" || ev.ToolName == "python_repl") && text != "":
		fmt.Fprintf(e.logs, "%s\n", text)
	}
}

func (e *primeEvents) fail(why string) {
	fmt.Fprintf(e.logs, "Error: %s\n", why)
	if e.err == "" {
		e.err = why
	}
}

// result is the run's error: prime-agent can exit 0 after a failed model
// call, so its own reason must fail the run.
func (e *primeEvents) result(err error) error {
	if e.err == "" && e.failed != "" {
		e.err = e.failed
	}
	if e.err == "" {
		return err
	}
	var exit *sandbox.ExitError
	if err == nil || errors.As(err, &exit) {
		return fmt.Errorf("prime: %s", e.err)
	}
	return fmt.Errorf("prime: %s: %w", e.err, err)
}
