package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/sandbox"
)

func TestPiArgs(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want []string
	}{
		{"defaults", Request{Prompt: "fix it"}, []string{"--mode", "json", "--", "fix it"}},
		// The family's first slash splits the provider from the model.
		{"model and effort", Request{Prompt: "fix it", Family: "openrouter/anthropic/claude-sonnet-5.5", Effort: "high"},
			[]string{"--mode", "json", "--provider", "openrouter", "--model", "anthropic/claude-sonnet-5.5", "--thinking", "high", "--", "fix it"}},
		{"follow-up, read-only", Request{Prompt: "plan", Family: "openai/gpt-5.5", Continue: true, ReadOnly: true},
			[]string{"--mode", "json", "--continue", "--tools", piReadOnlyTools, "--provider", "openai", "--model", "gpt-5.5", "--", "plan"}},
		{"dash prompt", Request{Prompt: "-x is this a flag?"}, []string{"--mode", "json", "--", "-x is this a flag?"}},
	}
	for _, tc := range tests {
		if got := piArgs(tc.req); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// Each provider's key goes to that provider's host only.
func TestPiProviders(t *testing.T) {
	p := &Pi{}
	for model, key := range map[string]string{
		"openrouter/anthropic/claude-sonnet-5.5": "OPENROUTER_API_KEY",
		"openai/gpt-5.5":                         "OPENAI_API_KEY",
		"anthropic/claude-opus-5-5":              "ANTHROPIC_API_KEY",
		"nope/model":                             "",
	} {
		if got := p.CredentialFor(model); got != key {
			t.Errorf("CredentialFor(%s) = %q, want %q", model, got, key)
		}
	}
	for key, host := range map[string]string{
		"OPENROUTER_API_KEY": "openrouter.ai",
		"OPENAI_API_KEY":     "api.openai.com",
		"ANTHROPIC_API_KEY":  "api.anthropic.com",
	} {
		s := p.SecretFor(key, "v")
		if s.EnvVar != key || s.Value != "v" || !slices.Equal(s.AllowHosts, []string{host}) {
			t.Errorf("SecretFor(%s) = %+v", key, s)
		}
	}
	if got := CredentialNames(p); got != "ANTHROPIC_API_KEY, OPENAI_API_KEY or OPENROUTER_API_KEY" {
		t.Errorf("CredentialNames = %q", got)
	}
}

// Lines as `pi --mode json` prints them (shortened; other events dropped).
const piRun = `{"type":"message_end","message":{"role":"user","content":"fix add"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"I'll read math.js."},{"type":"toolCall","name":"read"}],"stopReason":"toolUse"}}
{"type":"tool_execution_start","toolName":"read","args":{"path":"math.js"}}
{"type":"tool_execution_end","toolName":"read","result":{"content":[{"type":"text","text":"function add(a,b){ return a-b }"}]},"isError":false}
{"type":"tool_execution_start","toolName":"bash","args":{"command":"node -e 1"}}
{"type":"tool_execution_end","toolName":"bash","result":{"content":[{"type":"text","text":"ok\n"}]},"isError":false}
{"type":"tool_execution_start","toolName":"grep","args":{"pattern":"add"}}
{"type":"tool_execution_end","toolName":"grep","result":{"content":[{"type":"text","text":"rg not found"}]},"isError":true}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Fixed add in math.js."}],"stopReason":"stop"}}
`

func TestPiEventsLog(t *testing.T) {
	var logs strings.Builder
	ev := &piEvents{logs: &logs}
	// Written in odd pieces, as a stream arrives: lines are split anywhere.
	for i := 0; i < len(piRun); i += 7 {
		ev.Write([]byte(piRun[i:min(i+7, len(piRun))]))
	}
	ev.flush()
	want := `I'll read math.js.

→ read math.js
$ node -e 1
ok
→ grep add
✗ grep: rg not found
Fixed add in math.js.

`
	if logs.String() != want {
		t.Errorf("log:\n%s\nwant:\n%s", logs.String(), want)
	}
	if ev.reply != "Fixed add in math.js." {
		t.Errorf("reply = %q", ev.reply)
	}
	if err := ev.result(nil); err != nil {
		t.Errorf("result = %v", err)
	}
}

// pi retries a failed model call: each failed attempt is a message_end
// with stopReason "error", and only the last one decides the run.
func TestPiRetries(t *testing.T) {
	failed := `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"Connection error."}}` + "\n"
	retry := `{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"Connection error."}` + "\n"

	var logs strings.Builder
	ok := &piEvents{logs: &logs}
	ok.Write([]byte(failed + retry +
		`{"type":"auto_retry_end","success":true,"attempt":1}` + "\n" +
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stopReason":"stop"}}` + "\n"))
	if err := ok.result(nil); err != nil {
		t.Errorf("a retry that worked: %v", err)
	}
	if !strings.Contains(logs.String(), "! model call failed: Connection error.\n  retrying (attempt 1 of 3)\n") {
		t.Errorf("log:\n%s", logs.String())
	}

	gaveUp := &piEvents{logs: &logs}
	gaveUp.Write([]byte(failed + retry + failed +
		`{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"Connection error."}` + "\n"))
	for _, exit := range []error{nil, &sandbox.ExitError{Code: 1}} {
		if err := gaveUp.result(exit); err == nil || err.Error() != "pi: Connection error." {
			t.Errorf("result(%v) = %v", exit, err)
		}
	}
	transport := errors.New("vm gone")
	if err := gaveUp.result(transport); !errors.Is(err, transport) {
		t.Errorf("transport error lost: %v", err)
	}

	// A failure pi doesn't retry ends the run with no auto_retry_end.
	once := &piEvents{logs: &logs}
	once.Write([]byte(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"401 invalid key"}}`))
	once.flush()
	if err := once.result(nil); err == nil || err.Error() != "pi: 401 invalid key" {
		t.Errorf("no retry: %v", err)
	}
}

func TestPiNonEventLinesPassThrough(t *testing.T) {
	var logs strings.Builder
	ev := &piEvents{logs: &logs}
	ev.Write([]byte("\x1b[33mwarning: something\x1b[0m\n"))
	if logs.String() != "warning: something\n" {
		t.Errorf("log = %q", logs.String())
	}
}
