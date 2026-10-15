package task

import (
	"strings"
	"testing"
)

func TestAsksQuestions(t *testing.T) {
	tests := []struct {
		reply string
		want  bool
	}{
		{"## Questions\n1. Which file?\n2. Strict or lenient?", true},
		{"I need to know a couple of things.\n\n### Clarifying questions:\n- Which language?", true},
		{"# Questions\nShould it ignore case?", true},
		{"Added is_palindrome to script.sh. Any questions, let me know.", false},
		{"## Plan\n1. Add the function\n## Open\nNothing", false},
	}
	for _, tc := range tests {
		if got := asksQuestions(tc.reply); got != tc.want {
			t.Errorf("asksQuestions(%q) = %v, want %v", tc.reply, got, tc.want)
		}
	}
}

func TestGuidance(t *testing.T) {
	if got := withGuidance("add a test", false); !strings.HasPrefix(got, "add a test\n\n(From forgerun:") || !strings.Contains(got, `"## Questions"`) {
		t.Errorf("build turn: %q", got)
	}
	if got := withGuidance("add a test", true); !strings.Contains(got, "plan only. Don't change any files") {
		t.Errorf("plan turn: %q", got)
	}
}
