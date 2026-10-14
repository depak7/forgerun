package task

import (
	"context"
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

func (fakeAgent) ID() string                { return "fake" }
func (fakeAgent) Image() string             { return "depak7/forgerun-agent-cursor:dev" } // has git
func (fakeAgent) Secrets() []sandbox.Secret { return nil }
func (fakeAgent) StatePaths() []string      { return []string{"/root/.fake-agent"} }
func (fakeAgent) Run(ctx context.Context, sb *sandbox.Sandbox, req agent.Request, logs io.Writer) error {
	return sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", "echo hello from forgerun >> README"}, Dir: req.Dir}, logs)
}

func TestRunEndToEnd(t *testing.T) {
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot a real VM")
	}
	r := &Runner{Agent: fakeAgent{}, OutDir: t.TempDir(), CPUs: 1, MemMiB: 1024, Timeout: 5 * time.Minute, Stdout: os.Stdout}
	res, err := r.Run(context.Background(), New("https://github.com/octocat/Hello-World", "unused"))
	if err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(res.PatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "+hello from forgerun") {
		t.Errorf("patch missing our change:\n%s", patch)
	}
	log, _ := os.ReadFile(res.LogPath)
	if !strings.Contains(string(log), "running fake agent (model: auto)") {
		t.Errorf("log does not record the model:\n%s", log)
	}
}

// shellAgent runs a shell script in the repo as its "work".
type shellAgent struct{ script string }

func (shellAgent) ID() string                { return "fake" }
func (shellAgent) Image() string             { return "depak7/forgerun-agent-cursor:dev" }
func (shellAgent) Secrets() []sandbox.Secret { return nil }
func (shellAgent) StatePaths() []string      { return []string{"/root/.fake-agent"} }
func (a shellAgent) Run(ctx context.Context, sb *sandbox.Sandbox, req agent.Request, logs io.Writer) error {
	return sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", a.script}, Dir: req.Dir}, logs)
}

func idleRunner(t *testing.T, script string) *Runner {
	t.Helper()
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot a real VM")
	}
	return &Runner{Agent: shellAgent{script}, OutDir: t.TempDir(), CPUs: 1, MemMiB: 1024,
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
	if res.PatchPath == "" {
		t.Error("its work was lost")
	}
}
