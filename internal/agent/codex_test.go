package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/sandbox"
)

func TestCodexKeyGoesThroughSecrets(t *testing.T) {
	c := &Codex{}
	s := c.Secrets("key")
	if c.ID() != "codex" || c.Credential() != "OPENAI_API_KEY" ||
		len(s) != 1 || s[0].EnvVar != "OPENAI_API_KEY" || s[0].Value != "key" {
		t.Fatalf("Secrets() = %+v", s)
	}
	if !slices.Equal(s[0].AllowHosts, []string{"api.openai.com"}) {
		t.Errorf("key allowed for %v, want only api.openai.com", s[0].AllowHosts)
	}
	for _, arg := range codexCmd("/work/repo", codexArgs(Request{Prompt: "p"})).Args {
		if strings.Contains(arg, "key") {
			t.Errorf("the key appears in the command line: %q", arg)
		}
	}
}

func TestCodexArgs(t *testing.T) {
	base := []string{"exec", "--json", "--color", "never",
		"-c", `approval_policy="never"`, "-c", `sandbox_mode="workspace-write"`}
	tests := []struct {
		name string
		req  Request
		want []string
	}{
		{"defaults", Request{Prompt: "fix it"}, append(slices.Clone(base), "--", "fix it")},
		{"model and effort", Request{Prompt: "fix it", ModelID: "gpt-6-astra-high", Family: "gpt-6-astra", Effort: "high"},
			append(slices.Clone(base), "-m", "gpt-6-astra", "-c", `model_reasoning_effort="high"`, "--", "fix it")},
		{"read-only", Request{Prompt: "plan", ReadOnly: true},
			[]string{"exec", "--json", "--color", "never",
				"-c", `approval_policy="never"`, "-c", `sandbox_mode="read-only"`, "--", "plan"}},
		{"follow-up", Request{Prompt: "also add a test", Family: "gpt-6.1-sol", Continue: true},
			[]string{"exec", "--json", "--color", "never", "resume", "--last",
				"-c", `approval_policy="never"`, "-c", `sandbox_mode="workspace-write"`,
				"-m", "gpt-6.1-sol", "--", "also add a test"}},
		// The prompt comes after "--", so one starting with a dash isn't a flag.
		{"dash prompt", Request{Prompt: "--help me"}, append(slices.Clone(base), "--", "--help me")},
	}
	for _, tc := range tests {
		if got := codexArgs(tc.req); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// The wrapper must hand every argument to codex unchanged, however odd the
// prompt, with stdin closed. Checked with a fake codex on the host that
// prints what it receives.
func TestCodexCmdPassesArgumentsUnchanged(t *testing.T) {
	bin := t.TempDir()
	fake := `#!/bin/sh
if read -r _; then echo "stdin: open"; else echo "stdin: closed"; fi
for a in "$@"; do printf 'arg[%s]\n' "$a"; done
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := "fix \"this\"; echo $HOME `whoami` $(rm -rf /)\nsecond line 'quoted' \\ end"
	c := codexCmd(t.TempDir(), codexArgs(Request{Prompt: prompt, Family: "gpt-6-astra", Effort: "high"}))

	cmd := exec.Command(c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "stdin: closed\n"
	for _, a := range codexArgs(Request{Prompt: prompt, Family: "gpt-6-astra", Effort: "high"}) {
		want += "arg[" + a + "]\n"
	}
	if string(out) != want {
		t.Errorf("codex received:\n%s\nwant:\n%s", out, want)
	}
}

// Lines as `codex exec --json` prints them (shortened; the message, error
// and turn events verified against the CLI, the command and file shapes
// from its docs and binary).
const codexRun = `{"type":"thread.started","thread_id":"01a1bec9-e8fa-7a63-91ca-fcd1768e3d3b"}
{"type":"turn.started"}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc node -e \"console.log(1+1)\"","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"bash -lc node -e \"console.log(1+1)\"","aggregated_output":"2\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_2","type":"file_change","changes":[{"path":"/work/repo/math.js","kind":"update"}],"status":"completed"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Fixed add in math.js."}}
{"type":"turn.completed","usage":{"input_tokens":11,"cached_input_tokens":0,"output_tokens":7}}
`

func TestCodexEventsLog(t *testing.T) {
	var logs strings.Builder
	ev := &codexEvents{logs: &logs}
	// Written in odd pieces, as a stream arrives: lines are split anywhere.
	for i := 0; i < len(codexRun); i += 7 {
		ev.Write([]byte(codexRun[i:min(i+7, len(codexRun))]))
	}
	ev.flush()
	want := `$ bash -lc node -e "console.log(1+1)"
2
→ edit /work/repo/math.js
Fixed add in math.js.

`
	if logs.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", logs.String(), want)
	}
	// The reply is the agent's last message.
	if got := ev.reply; got != "Fixed add in math.js." {
		t.Errorf("reply = %q", got)
	}
	if err := ev.result(nil); err != nil {
		t.Errorf("result = %v", err)
	}
}

// A dropped connection is retried: the error events don't fail a run that
// then completes.
func TestCodexEventsReconnectThenCompletes(t *testing.T) {
	var logs strings.Builder
	ev := &codexEvents{logs: &logs}
	ev.Write([]byte(`{"type":"error","message":"Reconnecting... 2/5 (stream disconnected before completion)"}` + "\n"))
	ev.Write([]byte(`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"done"}}` + "\n"))
	ev.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}` + "\n"))
	ev.flush()
	if err := ev.result(nil); err != nil {
		t.Errorf("a completed run after reconnects: %v", err)
	}
	if !strings.Contains(logs.String(), "! Reconnecting... 2/5") {
		t.Errorf("log:\n%s", logs.String())
	}
}

// turn.failed's reason fails the run, whatever the exit code said; a run
// that died mid-reconnect is explained by its last error event.
func TestCodexEventsFailures(t *testing.T) {
	var logs strings.Builder
	ev := &codexEvents{logs: &logs}
	ev.Write([]byte(`{"type":"turn.failed","error":{"message":"stream error: exceeded retry limit for SSE stream"}}` + "\n"))
	ev.flush()
	for _, exit := range []error{nil, &sandbox.ExitError{Code: 1}} {
		err := ev.result(exit)
		if err == nil || err.Error() != "codex: stream error: exceeded retry limit for SSE stream" {
			t.Errorf("result(%v) = %v", exit, err)
		}
	}
	// A transport failure is kept, so callers can still see it.
	transport := errors.New("vm gone")
	if err := ev.result(transport); !errors.Is(err, transport) {
		t.Errorf("transport error lost: %v", err)
	}

	dead := &codexEvents{logs: &logs}
	dead.Write([]byte(`{"type":"error","message":"Reconnecting... 5/5 (Connection failed)"}` + "\n"))
	dead.flush()
	err := dead.result(&sandbox.ExitError{Code: 1})
	var exit *sandbox.ExitError
	if err == nil || !strings.Contains(err.Error(), "Reconnecting... 5/5") || !errors.As(err, &exit) {
		t.Errorf("died mid-reconnect: %v", err)
	}
	if err := dead.result(nil); err != nil {
		t.Errorf("no events, clean exit: %v", err)
	}
}

// A failed command shows its exit code and output; a long output is cut.
func TestCodexCommandFailureAndLongOutput(t *testing.T) {
	var logs strings.Builder
	ev := &codexEvents{logs: &logs}
	ev.Write([]byte(`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"bash -lc go test ./...","aggregated_output":"FAIL\tpkg","exit_code":1,"status":"failed"}}` + "\n"))
	if !strings.Contains(logs.String(), "✗ exit 1\nFAIL\tpkg\n") {
		t.Errorf("log:\n%s", logs.String())
	}
	logs.Reset()
	ev.Write([]byte(`{"type":"item.completed","item":{"id":"i2","type":"command_execution","command":"yes","aggregated_output":"` +
		strings.Repeat("y", 5000) + `","exit_code":0,"status":"completed"}}` + "\n"))
	if n := len(logs.String()); n > maxToolOutput+100 || !strings.HasSuffix(logs.String(), "… (output cut)\n") {
		t.Errorf("logged %d bytes: %q", n, logs.String()[n-40:])
	}
}
