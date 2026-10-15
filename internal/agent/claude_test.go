package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestClaudeCodeTokenGoesThroughSecrets(t *testing.T) {
	c := &ClaudeCode{}
	s := c.Secrets("tok")
	if c.ID() != "claude-code" || c.Credential() != "CLAUDE_CODE_OAUTH_TOKEN" ||
		len(s) != 1 || s[0].EnvVar != "CLAUDE_CODE_OAUTH_TOKEN" || s[0].Value != "tok" {
		t.Fatalf("Secrets() = %+v", s)
	}
	if !slices.Equal(s[0].AllowHosts, []string{"api.anthropic.com"}) {
		t.Errorf("token allowed for %v, want only api.anthropic.com", s[0].AllowHosts)
	}
	for _, arg := range claudeCmd("/work/repo", claudeArgs(Request{Prompt: "p"})).Args {
		if strings.Contains(arg, "tok") {
			t.Errorf("the token appears in the command line: %q", arg)
		}
	}
}

func TestClaudeArgs(t *testing.T) {
	base := []string{"-p", "fix it", "--permission-mode", "bypassPermissions", "--output-format", "text"}
	tests := []struct {
		family, effort string
		want           []string
	}{
		{"", "", base},
		{"opus", "", append(slices.Clone(base), "--model", "opus")},
		{"opus", "high", append(slices.Clone(base), "--model", "opus", "--effort", "high")},
	}
	for _, tc := range tests {
		got := claudeArgs(Request{Prompt: "fix it", ModelID: "ignored-" + tc.family, Family: tc.family, Effort: tc.effort})
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s/%s: got %v, want %v", tc.family, tc.effort, got, tc.want)
		}
	}
}

// The wrapper must hand every argument to claude unchanged, however odd the
// prompt, with stdin closed and IS_SANDBOX set. Checked with a fake claude
// on the host that prints what it receives.
func TestClaudeCmdPassesArgumentsUnchanged(t *testing.T) {
	bin := t.TempDir()
	fake := `#!/bin/sh
printf 'IS_SANDBOX=%s\n' "$IS_SANDBOX"
if read -r _; then echo "stdin: open"; else echo "stdin: closed"; fi
for a in "$@"; do printf 'arg[%s]\n' "$a"; done
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := "fix \"this\"; echo $HOME `whoami` $(rm -rf /)\nsecond line 'quoted' \\ end"
	c := claudeCmd(t.TempDir(), claudeArgs(Request{Prompt: prompt, Family: "opus", Effort: "high"}))

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
	want := "IS_SANDBOX=1\nstdin: closed\n"
	for _, a := range claudeArgs(Request{Prompt: prompt, Family: "opus", Effort: "high"}) {
		want += "arg[" + a + "]\n"
	}
	if string(out) != want {
		t.Errorf("claude received:\n%s\nwant:\n%s", out, want)
	}
}
