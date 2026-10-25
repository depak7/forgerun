package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/sandbox"
)

func TestOpenCodeKeyGoesThroughSecrets(t *testing.T) {
	o := &OpenCode{}
	s := o.Secrets("key")
	if o.Credential() != "OPENCODE_API_KEY" || len(s) != 1 || s[0].EnvVar != "OPENCODE_API_KEY" || s[0].Value != "key" {
		t.Fatalf("Secrets() = %+v", s)
	}
	if !slices.Equal(s[0].AllowHosts, []string{"opencode.ai"}) {
		t.Errorf("key allowed for %v, want only opencode.ai", s[0].AllowHosts)
	}
}

func TestOpenCodeArgs(t *testing.T) {
	base := []string{"run", "--format", "json", "--auto", "--print-logs", "--log-level", "ERROR"}
	tests := []struct {
		name string
		req  Request
		want []string
	}{
		{"defaults", Request{Prompt: "fix it"}, append(slices.Clone(base), "--", "fix it")},
		{"model", Request{Prompt: "fix it", ModelID: "big-pickle"},
			append(slices.Clone(base), "--model", "opencode/big-pickle", "--", "fix it")},
		{"follow-up, read-only, effort", Request{Prompt: "plan", ModelID: "gpt-5.3-codex", Effort: "high", Continue: true, ReadOnly: true},
			append(slices.Clone(base), "--agent", "plan", "--continue", "--model", "opencode/gpt-5.3-codex", "--variant", "high", "--", "plan")},
		// The prompt comes after "--", so one starting with a dash isn't a flag.
		{"dash prompt", Request{Prompt: "--help me"}, append(slices.Clone(base), "--", "--help me")},
	}
	for _, tc := range tests {
		if got := opencodeArgs(tc.req); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// Lines as `opencode run --format json` prints them (shortened).
const opencodeRun = `{"type":"step_start","part":{"type":"step-start"}}
{"type":"text","part":{"type":"text","text":"I'll start by reading math.js."}}
{"type":"tool_use","part":{"type":"tool","tool":"read","state":{"status":"completed","input":{"filePath":"/work/repo/math.js"},"output":"1: function add(a,b){ return a-b }","title":"math.js"}}}
{"type":"tool_use","part":{"type":"tool","tool":"edit","state":{"status":"completed","input":{"filePath":"/work/repo/math.js"},"output":"Edit applied successfully.","title":"math.js"}}}
{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"node -e \"console.log(require('./math').add(2,3))\""},"output":"5\n","title":"node -e"}}}
{"type":"tool_use","part":{"type":"tool","tool":"grep","state":{"status":"error","input":{"pattern":"add"},"error":"ripgrep execution failed"}}}
{"type":"step_finish","part":{"type":"step-finish","reason":"tool-calls"}}
{"type":"text","part":{"type":"text","text":"Fixed add in math.js."}}
{"type":"text","part":{"type":"text","text":"## Questions\n- Should I add tests?"}}
{"type":"step_finish","part":{"type":"step-finish","reason":"stop"}}
`

func TestOpenCodeEventsLog(t *testing.T) {
	var logs strings.Builder
	ev := &opencodeEvents{logs: &logs}
	// Written in odd pieces, as a stream arrives: lines are split anywhere.
	for i := 0; i < len(opencodeRun); i += 7 {
		ev.Write([]byte(opencodeRun[i:min(i+7, len(opencodeRun))]))
	}
	ev.flush()
	want := `I'll start by reading math.js.

→ read math.js
→ edit math.js
$ node -e "console.log(require('./math').add(2,3))"
5
✗ grep add: ripgrep execution failed
Fixed add in math.js.

## Questions
- Should I add tests?

`
	if logs.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", logs.String(), want)
	}
	// The reply is what it said after its last tool call.
	if got := ev.reply(); got != "Fixed add in math.js.\n\n## Questions\n- Should I add tests?" {
		t.Errorf("reply = %q", got)
	}
	if err := ev.result(nil); err != nil {
		t.Errorf("result = %v", err)
	}
}

func TestOpenCodeEventsErrors(t *testing.T) {
	var logs strings.Builder
	ev := &opencodeEvents{logs: &logs}
	ev.Write([]byte("\x1b[0m> build · deepseek\x1b[0m\n" +
		`{"type":"error","error":{"name":"APIError","data":{"message":"Upstream request failed: Insufficient account funds","statusCode":402}}}`))
	ev.flush()
	if !strings.Contains(logs.String(), "> build · deepseek\n") || strings.Contains(logs.String(), "\x1b") {
		t.Errorf("a plain line should pass through without colour codes: %q", logs.String())
	}
	// The error event explains the failure, whatever the exit code said.
	for _, exit := range []error{nil, &sandbox.ExitError{Code: 1}} {
		err := ev.result(exit)
		if err == nil || err.Error() != "opencode: Upstream request failed: Insufficient account funds" {
			t.Errorf("result(%v) = %v", exit, err)
		}
	}
	// A transport failure is kept, so callers can still see it.
	transport := errors.New("vm gone")
	if err := ev.result(transport); !errors.Is(err, transport) {
		t.Errorf("transport error lost: %v", err)
	}
}

func TestOpenCodeLongShellOutputIsCut(t *testing.T) {
	var logs strings.Builder
	ev := &opencodeEvents{logs: &logs}
	ev.Write([]byte(`{"type":"tool_use","part":{"tool":"bash","state":{"status":"completed","input":{"command":"yes"},"output":"` +
		strings.Repeat("y", 5000) + `"}}}` + "\n"))
	if n := len(logs.String()); n > maxToolOutput+100 || !strings.HasSuffix(logs.String(), "… (output cut)\n") {
		t.Errorf("logged %d bytes: %q", n, logs.String()[n-40:])
	}
}

// The watchdog watches an agent's state paths, except OpenCode's: it keeps
// writing its own logs while stuck retrying.
func TestActivityPaths(t *testing.T) {
	if got := ActivityPaths(&ClaudeCode{}); !slices.Equal(got, (&ClaudeCode{}).StatePaths()) {
		t.Errorf("claude-code watches %v", got)
	}
	if got := ActivityPaths(&OpenCode{}); len(got) != 0 {
		t.Errorf("opencode watches %v", got)
	}
}

// logSandbox answers every command with out: OpenCode's log, as grep finds it.
type logSandbox struct {
	sandbox.Sandbox
	out string
}

func (s logSandbox) Run(context.Context, sandbox.Cmd) (string, error) { return s.out, nil }

func TestLastLoggedError(t *testing.T) {
	sb := logSandbox{out: `error.error="AI_APICallError: Free usage exceeded, please try again later"` + "\n"}
	if got := lastLoggedError(sb); got != "Free usage exceeded, please try again later" {
		t.Errorf("got %q", got)
	}
	if got := lastLoggedError(logSandbox{}); got != "" {
		t.Errorf("no log: %q", got)
	}
}

// A logged line as OpenCode prints it with --print-logs --log-level ERROR.
func errLine(why string) string {
	return `timestamp=2026-10-24T18:23:21.878Z level=ERROR run=961b9ee2 message="stream error" providerID=opencode modelID=big-pickle error.error="` + why + `"` + "\n"
}

// A failure retrying can't fix stops OpenCode at once, with the reason.
func TestOpenCodeStopsOnHopelessFailures(t *testing.T) {
	var logs strings.Builder
	stops := 0
	ev := &opencodeEvents{logs: &logs, stop: func(string) { stops++ }}
	ev.Write([]byte(errLine("AI_APICallError: Free usage exceeded, please try again later")))
	ev.Write([]byte(errLine("AI_APICallError: Free usage exceeded, please try again later")))
	if stops != 1 {
		t.Errorf("stopped %d times, want 1", stops)
	}
	if err := ev.result(nil); err == nil || err.Error() != "opencode: Free usage exceeded, please try again later" {
		t.Errorf("result = %v", err)
	}
	if !strings.Contains(logs.String(), "! model call failed (1): Free usage exceeded") || strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("log:\n%s", logs.String())
	}
}

// Other failures get a few retries first.
func TestOpenCodeStopsAfterRepeatedFailures(t *testing.T) {
	var logs strings.Builder
	stopped := ""
	ev := &opencodeEvents{logs: &logs, stop: func(r string) { stopped = r }}
	for i := 1; i < maxFailedCalls; i++ {
		ev.Write([]byte(errLine(`AI_APICallError: Bad gateway`)))
	}
	if stopped != "" {
		t.Fatalf("stopped after %d failures: %q", maxFailedCalls-1, stopped)
	}
	ev.Write([]byte(errLine(`AI_APICallError: Bad gateway`)))
	if want := "5 model calls failed; the last: Bad gateway"; stopped != want {
		t.Errorf("stopped = %q, want %q", stopped, want)
	}
}
