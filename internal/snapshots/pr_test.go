package snapshots

import (
	"context"
	"errors"
	"io"
	"os"
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

func (f *fakePublisher) Publish(_ context.Context, req task.PublishRequest) (string, error) {
	f.got = append(f.got, req)
	if f.err != nil {
		return "", f.err
	}
	return "https://github.com/o/r/pull/1", nil
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
