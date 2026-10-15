package snapshots

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/task"
)

// Continue makes messyAgent a Continuer. Its "summary" quotes the agent's
// chat file, which only exists until the snapshot cleanup deletes it.
func (messyAgent) Continue(ctx context.Context, sb *sandbox.Sandbox, dir, prompt string) (string, error) {
	if !strings.Contains(prompt, "## Requested") {
		return "", errors.New("not forgerun's summary prompt")
	}
	chat, err := sb.Run(ctx, sandbox.Cmd{Name: "cat", Args: []string{"/root/.fake-agent/history"}})
	if err != nil {
		return "", err
	}
	return "## Requested\nedit README\n\n## Done\nchat said: " + strings.TrimSpace(chat), nil
}

// plainAgent hides messyAgent's Continue: embedding the Agent interface
// only promotes the interface's methods.
type plainAgent struct{ agent.Agent }

// idleAgent changes nothing.
type idleAgent struct{ messyAgent }

func (idleAgent) Run(context.Context, *sandbox.Sandbox, agent.Request, io.Writer) error { return nil }

// fakePublisher records what it was asked to publish.
type fakePublisher struct {
	got []task.PublishRequest
	err error
}

func (f *fakePublisher) Publish(_ context.Context, req task.PublishRequest) (task.PublishedPR, error) {
	f.got = append(f.got, req)
	if f.err != nil {
		return task.PublishedPR{}, f.err
	}
	return task.PublishedPR{URL: "https://github.com/o/r/pull/1", Number: 1, Branch: "forgerun/" + req.TaskID}, nil
}

func prTask() task.Task {
	t := task.New(testRepo, "edit the README")
	t.PR = true
	return t
}

func TestPullRequestTask(t *testing.T) {
	r, _ := setup(t, messyAgent{})
	pub := &fakePublisher{}
	r.Publisher = pub

	res, err := r.Run(context.Background(), prTask())
	if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(res.LogPath)
	// The summary was taken while the agent's chat still existed.
	if !strings.Contains(res.Summary, "chat said: chat") {
		t.Errorf("summary = %q\nlog:\n%s", res.Summary, log)
	}
	if saved, _ := os.ReadFile(res.SummaryPath); strings.TrimSpace(string(saved)) != res.Summary {
		t.Errorf("summary.md = %q", saved)
	}
	if res.PRURL != "https://github.com/o/r/pull/1" || res.PRError != "" {
		t.Errorf("pr url %q, error %q", res.PRURL, res.PRError)
	}
	if len(pub.got) != 1 {
		t.Fatalf("published %d times", len(pub.got))
	}
	got := pub.got[0]
	if got.BaseCommit == "" || got.BaseCommit != res.BaseCommit || got.PatchPath != res.PatchPath ||
		got.Summary != res.Summary || got.Prompt != "edit the README" || got.Repo != testRepo {
		t.Errorf("publish request = %+v", got)
	}
	// Summary, then snapshot (which wipes the chat), then publish.
	order := []string{"asking the agent for a summary", "cleaning VM for snapshot", "pull request opened"}
	last := -1
	for _, step := range order {
		i := strings.Index(string(log), step)
		if i < last {
			t.Errorf("%q is out of order in:\n%s", step, log)
		}
		last = i
	}
}

func TestPullRequestWithoutChanges(t *testing.T) {
	r, _ := setup(t, idleAgent{})
	pub := &fakePublisher{}
	r.Publisher = pub
	res, err := r.Run(context.Background(), prTask())
	if err != nil {
		t.Fatal(err)
	}
	if len(pub.got) != 0 || !strings.Contains(res.PRError, "made no changes") || res.Summary != "" {
		t.Errorf("published %d, pr error %q, summary %q", len(pub.got), res.PRError, res.Summary)
	}
}

func TestPullRequestFailureKeepsTheTask(t *testing.T) {
	r, _ := setup(t, messyAgent{})
	r.Publisher = &fakePublisher{err: errors.New("GitHub POST /pulls: HTTP 403: denied")}
	res, err := r.Run(context.Background(), prTask())
	if err != nil {
		t.Fatalf("task failed because publishing did: %v", err)
	}
	if res.PatchPath == "" || !strings.Contains(res.PRError, "HTTP 403") || res.PRURL != "" {
		t.Errorf("patch %q, pr error %q, url %q", res.PatchPath, res.PRError, res.PRURL)
	}
}

func TestPullRequestFromAgentWithoutContinue(t *testing.T) {
	r, _ := setup(t, plainAgent{messyAgent{}})
	pub := &fakePublisher{}
	r.Publisher = pub
	res, err := r.Run(context.Background(), prTask())
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "" || len(pub.got) != 1 || pub.got[0].Summary != "" || res.PRURL == "" {
		t.Errorf("summary %q, published %d, url %q", res.Summary, len(pub.got), res.PRURL)
	}
	if log, _ := os.ReadFile(res.LogPath); !strings.Contains(string(log), "can't continue its conversation") {
		t.Errorf("log doesn't explain the missing summary:\n%s", log)
	}
}

func TestPullRequestsDisabled(t *testing.T) {
	r, _ := setup(t, messyAgent{})
	res, err := r.Run(context.Background(), prTask())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.PRError, "disabled") {
		t.Errorf("pr error %q", res.PRError)
	}
}

// promptAgent records what it was asked and where it ran, then edits a file.
type promptAgent struct{ asked *[]string }

func (promptAgent) ID() string                      { return "fake" }
func (promptAgent) Image() string                   { return "depak7/forgerun-agent-cursor:dev" }
func (promptAgent) Credential() string              { return "" }
func (promptAgent) Secrets(string) []sandbox.Secret { return nil }
func (promptAgent) StatePaths() []string            { return nil }
func (a promptAgent) Run(ctx context.Context, sb *sandbox.Sandbox, req agent.Request, logs io.Writer) error {
	*a.asked = append(*a.asked, req.Prompt)
	// Show the branch, prove the diff command from the prompt works, then edit.
	return sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c",
		`echo "on branch $(git rev-parse --abbrev-ref HEAD)"; git diff --stat ` + threadBase + ` HEAD | tail -1; echo more >> README`}, Dir: req.Dir}, logs)
}
func (a promptAgent) Continue(ctx context.Context, sb *sandbox.Sandbox, dir, prompt string) (string, error) {
	*a.asked = append(*a.asked, prompt)
	return "## Requested\nboth\n\n## Done\nboth", nil
}

const threadBase = "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d" // Hello-World's master

// A follow-up works on the thread's branch, gets the thread's context before
// the new request, asks for a whole-thread summary, and publishes to the
// same pull request. The snapshot it leaves doesn't keep the branch.
func TestFollowUpTask(t *testing.T) {
	var asked []string
	r, s := setup(t, promptAgent{&asked})
	pub := &fakePublisher{}
	r.Publisher = pub

	tk := task.New(testRepo, "rename greet to sayHello")
	tk.PR, tk.ParentID = true, "t-first"
	tk.FollowUp = &task.FollowUp{Branch: "test", PRNumber: 9, ThreadBase: threadBase,
		Summary: "SUMMARY OF EARLIER WORK", Requests: []string{"add greet"}}
	res, err := r.Run(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(res.LogPath)
	if !strings.Contains(string(log), "on branch test") || !strings.Contains(string(log), "changed") {
		t.Errorf("agent didn't run on the thread's branch with a working diff:\n%s", log)
	}
	if len(asked) != 2 {
		t.Fatalf("agent asked %d times", len(asked))
	}
	p := asked[0]
	ctxAt, reqAt := strings.Index(p, "SUMMARY OF EARLIER WORK"), strings.Index(p, "rename greet to sayHello")
	if ctxAt < 0 || reqAt < ctxAt || !strings.HasSuffix(p, "rename greet to sayHello") || !strings.Contains(p, "git diff "+threadBase+" HEAD") {
		t.Errorf("prompt should carry the context first and the request last:\n%s", p)
	}
	if !strings.Contains(asked[1], "Cover the whole pull request") {
		t.Errorf("summary request doesn't ask for the whole thread:\n%s", asked[1])
	}
	if len(pub.got) != 1 {
		t.Fatalf("published %d times", len(pub.got))
	}
	got := pub.got[0]
	if got.Branch != "test" || got.PRNumber != 9 || !slices.Equal(got.Requests, []string{"add greet", "rename greet to sayHello"}) ||
		got.BaseCommit != res.BaseCommit {
		t.Errorf("publish request = %+v", got)
	}
	if head := sh(t, "git ls-remote "+testRepo+" refs/heads/test | cut -f1"); res.BaseCommit != head {
		t.Errorf("start commit %s, want the branch head %s", res.BaseCommit, head)
	}

	// The snapshot is back on master, without the thread's branch.
	snap, err := s.CurrentSnapshot(context.Background(), testRepo, "fake")
	if err != nil {
		t.Fatal(err)
	}
	vm, err := sandbox.Restore(context.Background(), snap.Ref, "fucheck-"+tk.ID[len(tk.ID)-6:], sandbox.Config{CPUs: 1, MemoryMiB: 512, MaxLifetime: 2 * 60e9})
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Destroy()
	branches, _ := vm.Run(context.Background(), sandbox.Cmd{Name: "git", Args: []string{"-C", "/work/repo", "branch", "--format=%(refname:short)"}})
	if strings.TrimSpace(branches) != "master" {
		t.Errorf("snapshot branches = %q, want only master", branches)
	}
}

// sh runs a host shell command and returns its trimmed output.
func sh(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("%s: %v", script, err)
	}
	return strings.TrimSpace(string(out))
}
