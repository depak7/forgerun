package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/sandbox"
)

func TestPrimeArgs(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want []string
	}{
		{"defaults", Request{Prompt: "fix it"},
			[]string{"--mode", "json", "--offline", "--no-skills", "--", "fix it"}},
		{"model and effort", Request{Prompt: "fix it", Family: "openrouter/anthropic/claude-sonnet-4.5", Effort: "high"},
			[]string{"--mode", "json", "--offline", "--no-skills", "--provider", "openrouter", "--model", "anthropic/claude-sonnet-4.5", "--thinking", "high", "--", "fix it"}},
		{"prime-inference", Request{Prompt: "fix it", Family: "prime-inference/z-ai/glm-5.3"},
			[]string{"--mode", "json", "--offline", "--no-skills", "--provider", "prime-inference", "--model", "z-ai/glm-5.3", "--", "fix it"}},
		{"follow-up, read-only", Request{Prompt: "plan", Family: "openai/gpt-5.5", Continue: true, ReadOnly: true},
			[]string{"--mode", "json", "--offline", "--no-skills", "--continue", "--append-system-prompt", primeReadOnlyPrompt, "--provider", "openai", "--model", "gpt-5.5", "--", "plan"}},
		{"dash prompt", Request{Prompt: "-x is this a flag?"},
			[]string{"--mode", "json", "--offline", "--no-skills", "--", "-x is this a flag?"}},
	}
	for _, tc := range tests {
		if got := primeArgs(tc.req); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestPrimeProviders(t *testing.T) {
	p := &Prime{}
	for model, key := range map[string]string{
		"openrouter/anthropic/claude-sonnet-4.5": "OPENROUTER_API_KEY",
		"openai/gpt-5.5":                         "OPENAI_API_KEY",
		"anthropic/claude-opus-5-5":              "ANTHROPIC_API_KEY",
		"google/gemini-2.5-flash":                "GEMINI_API_KEY",
		"xai/grok-4.6":                           "XAI_API_KEY",
		"prime-inference/z-ai/glm-5.3":           "PRIME_API_KEY",
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
		"GEMINI_API_KEY":     "generativelanguage.googleapis.com",
		"XAI_API_KEY":        "api.x.ai",
		"PRIME_API_KEY":      "api.pinference.ai",
	} {
		s := p.SecretFor(key, "v")
		if s.EnvVar != key || s.Value != "v" || !slices.Equal(s.AllowHosts, []string{host}) {
			t.Errorf("SecretFor(%s) = %+v", key, s)
		}
	}
	wantNames := "ANTHROPIC_API_KEY, GEMINI_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY, PRIME_API_KEY or XAI_API_KEY"
	if got := CredentialNames(p); got != wantNames {
		t.Errorf("CredentialNames = %q, want %q", got, wantNames)
	}
	if p.ID() != "prime" || p.Image() != "depak7/forgerun-agent-prime:dev" {
		t.Errorf("id/image = %s %s", p.ID(), p.Image())
	}
	if !slices.Equal(p.StatePaths(), []string{"/root/.prime"}) {
		t.Errorf("StatePaths = %v", p.StatePaths())
	}
}

const primeRun = `{"type":"message_end","message":{"role":"user","content":"fix add"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"I'll inspect math.js."},{"type":"toolCall","name":"ipython"}],"stopReason":"toolUse"}}
{"type":"tool_execution_start","toolName":"python_repl","args":{"code":"print(open('math.js').read())"}}
{"type":"tool_execution_end","toolName":"python_repl","result":{"content":[{"type":"text","text":"function add(a,b){ return a-b }"}]},"isError":false}
{"type":"tool_execution_start","toolName":"bash","args":{"command":"node -e 1"}}
{"type":"tool_execution_end","toolName":"bash","result":{"content":[{"type":"text","text":"ok\n"}]},"isError":false}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Fixed add in math.js."}],"stopReason":"stop"}}
`

func TestPrimeEventsLog(t *testing.T) {
	var logs strings.Builder
	ev := &piEvents{agent: "prime", logs: &logs}
	for i := 0; i < len(primeRun); i += 7 {
		ev.Write([]byte(primeRun[i:min(i+7, len(primeRun))]))
	}
	ev.flush()
	want := `I'll inspect math.js.

→ python_repl print(open('math.js').read())
function add(a,b){ return a-b }
$ node -e 1
ok
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

func TestPrimeAuthFailureDespiteExitZero(t *testing.T) {
	var logs strings.Builder
	ev := &piEvents{agent: "prime", logs: &logs}
	ev.Write([]byte(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"Provider authentication failed (authentication_error, 401): invalid x-api-key"}}`))
	ev.flush()
	for _, exit := range []error{nil, &sandbox.ExitError{Code: 0}, &sandbox.ExitError{Code: 1}} {
		if err := ev.result(exit); err == nil || !strings.Contains(err.Error(), "Provider authentication failed") {
			t.Errorf("result(%v) = %v", exit, err)
		}
	}
	transport := errors.New("vm gone")
	if err := ev.result(transport); !errors.Is(err, transport) {
		t.Errorf("transport error lost: %v", err)
	}
}

func TestPrimeNonEventLinesPassThrough(t *testing.T) {
	var logs strings.Builder
	ev := &piEvents{agent: "prime", logs: &logs}
	ev.Write([]byte("› setting up python kernel\n"))
	if logs.String() != "› setting up python kernel\n" {
		t.Errorf("log = %q", logs.String())
	}
}
