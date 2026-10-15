package task

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/sandbox"
)

func TestNewIDsAreUnique(t *testing.T) {
	a, b := New("r", "p"), New("r", "p")
	if a.ID == b.ID {
		t.Fatalf("two tasks got the same ID %q", a.ID)
	}
	if !strings.HasPrefix(a.ID, "t-") {
		t.Errorf("ID %q should start with t-", a.ID)
	}
}

// fakeAgent "edits" the repo with a shell command instead of calling an AI.
type fakeAgent struct{}

func (fakeAgent) ID() string                      { return "fake" }
func (fakeAgent) Image() string                   { return "depak7/forgerun-agent-cursor:dev" } // has git
func (fakeAgent) Credential() string              { return "" }
func (fakeAgent) Secrets(string) []sandbox.Secret { return nil }
func (fakeAgent) StatePaths() []string            { return []string{"/root/.fake-agent"} }
func (fakeAgent) Run(ctx context.Context, sb *sandbox.Sandbox, req agent.Request, logs io.Writer) error {
	return sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", "echo hello from forgerun >> README"}, Dir: req.Dir}, logs)
}

func TestRunEndToEnd(t *testing.T) {
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot a real VM")
	}
	r := &Runner{Agents: agent.NewSet(nil, fakeAgent{}), DefaultAgent: "fake", OutDir: t.TempDir(), CPUs: 1, MemMiB: 1024, Timeout: 5 * time.Minute, Stdout: os.Stdout}
	res, err := r.Run(context.Background(), New("https://github.com/octocat/Hello-World", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Diff, "+hello from forgerun") {
		t.Errorf("diff missing our change:\n%s", res.Diff)
	}
	log, _ := os.ReadFile(res.LogPath)
	if !strings.Contains(string(log), "running fake agent (model: auto)") {
		t.Errorf("log does not record the model:\n%s", log)
	}
}

// shellAgent runs a shell script in the repo as its "work".
type shellAgent struct{ script string }

func (shellAgent) ID() string                      { return "fake" }
func (shellAgent) Image() string                   { return "depak7/forgerun-agent-cursor:dev" }
func (shellAgent) Credential() string              { return "" }
func (shellAgent) Secrets(string) []sandbox.Secret { return nil }
func (shellAgent) StatePaths() []string            { return []string{"/root/.fake-agent"} }
func (a shellAgent) Run(ctx context.Context, sb *sandbox.Sandbox, req agent.Request, logs io.Writer) error {
	return sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", a.script}, Dir: req.Dir}, logs)
}

func idleRunner(t *testing.T, script string) *Runner {
	t.Helper()
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot a real VM")
	}
	return &Runner{Agents: agent.NewSet(nil, shellAgent{script}), DefaultAgent: "fake", OutDir: t.TempDir(), CPUs: 1, MemMiB: 1024,
		Timeout: 5 * time.Minute, AgentIdle: 4 * time.Second, Stdout: io.Discard}
}

func TestStalledAgentIsStopped(t *testing.T) {
	r := idleRunner(t, `echo "Connection lost, reconnecting (attempt 1)..."; sleep 60`)
	start := time.Now()
	res, err := r.Run(context.Background(), New("https://github.com/octocat/Hello-World", "unused"))
	if err == nil || !strings.Contains(err.Error(), "agent stalled: no output or file changes for 4s") ||
		!strings.Contains(err.Error(), "Connection lost, reconnecting") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 40*time.Second {
		t.Errorf("took %s; the stall should be caught long before the agent's 60s sleep ends", took)
	}
	if log, _ := os.ReadFile(res.LogPath); !strings.Contains(string(log), "stopped the agent") {
		t.Errorf("log doesn't say why:\n%s", log)
	}
}

func TestQuietButBusyAgentIsNotStopped(t *testing.T) {
	// No output for 12s (three times the limit), but a file changes every second.
	r := idleRunner(t, `for i in $(seq 12); do echo $i >> progress.txt; sleep 1; done`)
	res, err := r.Run(context.Background(), New("https://github.com/octocat/Hello-World", "unused"))
	if err != nil {
		t.Fatalf("a busy agent was stopped: %v", err)
	}
	if res.Diff == "" {
		t.Error("its work was lost")
	}
}

// keyAgent needs a credential called FAKE_KEY and reports what it sees.
type keyAgent struct{ id string }

func (a keyAgent) ID() string       { return a.id }
func (keyAgent) Image() string      { return "depak7/forgerun-agent-cursor:dev" }
func (keyAgent) Credential() string { return "FAKE_KEY" }
func (keyAgent) Secrets(v string) []sandbox.Secret {
	return []sandbox.Secret{{EnvVar: "FAKE_KEY", Value: v, AllowHosts: []string{"example.invalid"}}}
}
func (keyAgent) StatePaths() []string { return nil }
func (a keyAgent) Run(ctx context.Context, sb *sandbox.Sandbox, req agent.Request, logs io.Writer) error {
	return sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c",
		`echo "` + a.id + ` sees FAKE_KEY=$FAKE_KEY"; echo x >> README`}, Dir: req.Dir}, logs)
}

// The task's own agent runs, with its credential fetched for the task; a
// missing credential fails only that task, before any VM is made.
func TestRunsTheTasksAgent(t *testing.T) {
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot a real VM")
	}
	newRunner := func(creds agent.Credentials) *Runner {
		return &Runner{Agents: agent.NewSet(creds, keyAgent{"first"}, keyAgent{"second"}), DefaultAgent: "first",
			OutDir: t.TempDir(), CPUs: 1, MemMiB: 1024, Timeout: 5 * time.Minute, Stdout: io.Discard}
	}
	tk := New("https://github.com/octocat/Hello-World", "unused")
	tk.AgentID = "second"
	res, err := newRunner(agent.StaticCredentials{"FAKE_KEY": "real-secret-value"}).Run(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(res.LogPath)
	if !strings.Contains(string(log), "running second agent") || !strings.Contains(string(log), "second sees FAKE_KEY=$MSB_FAKE_KEY") {
		t.Errorf("wrong agent, or the credential wasn't injected as a placeholder:\n%s", log)
	}
	if strings.Contains(string(log), "real-secret-value") {
		t.Error("the real credential reached the VM")
	}

	start := time.Now()
	_, err = newRunner(agent.StaticCredentials{}).Run(context.Background(), tk)
	if !errors.Is(err, agent.ErrNoCredential) || !strings.Contains(err.Error(), "set FAKE_KEY") {
		t.Errorf("missing credential: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %s: a missing credential should fail before booting a VM", took)
	}
}
