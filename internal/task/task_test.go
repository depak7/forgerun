package task

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

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

func (fakeAgent) Name() string              { return "fake" }
func (fakeAgent) Image() string             { return "depak7/forgerun-agent-cursor:dev" } // has git
func (fakeAgent) Secrets() []sandbox.Secret { return nil }
func (fakeAgent) Run(ctx context.Context, sb *sandbox.Sandbox, dir, prompt string, logs io.Writer) error {
	return sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", "echo hello from forgerun >> README"}, Dir: dir}, logs)
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
}
