package snapshots

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/publish"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/task"
)

// turnAgent adds one file per turn and reports what it was given: whether
// it was asked to continue its conversation, the start of its prompt, and
// the files already in the checkout. It's a Continuer.
type turnAgent struct{ messyAgent }

func (turnAgent) Run(ctx context.Context, sb sandbox.Sandbox, req agent.Request, logs io.Writer) error {
	first, _, _ := strings.Cut(req.Prompt, "\n")
	script := fmt.Sprintf(`set -e
echo "continue=%v"
echo "prompt starts: %s"
echo "turn files: $(ls turn-*.txt 2>/dev/null | tr '\n' ' ')"
echo "origin: $(git remote get-url origin)"
echo "credentials: $(cat .git/config ~/.gitconfig ~/.git-credentials 2>/dev/null | grep -c -i -E 'extraheader|tok-never|x-access-token' || true)"
n=$(ls turn-*.txt 2>/dev/null | wc -l)
echo made > turn-$((n+1)).txt`, req.Continue, strings.ReplaceAll(first, `"`, ""))
	return sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", script}, Dir: req.Dir}, logs)
}

func (turnAgent) Continue(context.Context, sandbox.Sandbox, string, string) (string, error) {
	return "summary", nil
}

func runTurn(t *testing.T, r *task.Runner, tk task.Task) (task.Result, string) {
	t.Helper()
	res, err := r.Run(context.Background(), tk)
	log, _ := os.ReadFile(res.LogPath)
	if err != nil {
		t.Fatalf("turn %s: %v\n%s", tk.Prompt, err, log)
	}
	return res, string(log)
}

// The second turn of a thread runs in the same VM, carrying on the agent's
// conversation; its diff holds only its own changes.
func TestSessionKeepsItsVM(t *testing.T) {
	r, _ := setup(t, turnAgent{})
	defer r.CloseAll()
	first := task.New(testRepo, "one")
	res1, _ := runTurn(t, r, first)
	second := task.New(testRepo, "two")
	second.ThreadID, second.ParentID, second.ThreadBase = first.ID, first.ID, res1.BaseCommit
	second.History = func(context.Context) ([]task.Turn, error) {
		t.Error("history loaded for an open session")
		return nil, nil
	}
	res2, log2 := runTurn(t, r, second)
	for _, want := range []string{"continuing session", "continue=true", "turn files: turn-1.txt"} {
		if !strings.Contains(log2, want) {
			t.Errorf("second turn's log lacks %q:\n%s", want, log2)
		}
	}
	if !strings.Contains(res2.Diff, "turn-2.txt") || strings.Contains(res2.Diff, "turn-1.txt") {
		t.Errorf("second turn's diff:\n%s", res2.Diff)
	}
	if got, err := r.Summarize(context.Background(), first.ID); err != nil || got != "summary" {
		t.Errorf("Summarize = %q, %v", got, err)
	}
}

// After the session closes, the next turn rebuilds it from the thread's
// start commit and the earlier turns' diffs, and replays the conversation.
func TestClosedSessionIsRebuilt(t *testing.T) {
	r, _ := setup(t, turnAgent{})
	defer r.CloseAll()
	first := task.New(testRepo, "one")
	res1, _ := runTurn(t, r, first)
	if err := r.CloseSession(first.ID); err != nil {
		t.Fatal(err)
	}
	if r.Session(first.ID).Open {
		t.Fatal("session still open after CloseSession")
	}

	second := task.New(testRepo, "two")
	second.ThreadID, second.ParentID, second.ThreadBase = first.ID, first.ID, res1.BaseCommit
	second.History = func(context.Context) ([]task.Turn, error) {
		return []task.Turn{{Prompt: "one", Reply: res1.Reply, Diff: res1.Diff}}, nil
	}
	res2, log2 := runTurn(t, r, second)
	for _, want := range []string{"rebuilding the thread's work (1 earlier turn(s))", "continue=false",
		"prompt starts: You are continuing a conversation", "turn files: turn-1.txt"} {
		if !strings.Contains(log2, want) {
			t.Errorf("rebuilt turn's log lacks %q:\n%s", want, log2)
		}
	}
	if !strings.Contains(res2.Diff, "turn-2.txt") || strings.Contains(res2.Diff, "turn-1.txt") {
		t.Errorf("rebuilt turn's diff:\n%s", res2.Diff)
	}
}

// A private repo: the host fetches it, and the VM never holds a credential.
// The first checkout, a warm restore and a session rebuild all go through
// the host. The "private" remote is a local bare copy of the test repo.
func TestPrivateRepoComesFromTheHost(t *testing.T) {
	r, _ := setup(t, turnAgent{})
	defer r.CloseAll()
	bare := t.TempDir() + "/remote.git"
	if out, err := exec.Command("git", "clone", "-q", "--bare", "--depth=1", testRepo, bare).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	pub := &publish.Publisher{Tokens: forge.NewStaticToken(forge.NewToken("tok-never-in-the-vm")),
		RemoteURL: func(forge.Repo) string { return "file://" + bare }}
	fetch := pub.Fetcher(forge.Token{})

	first := task.New(testRepo, "one")
	first.Fetch = fetch
	res1, log1 := runTurn(t, r, first)
	if !strings.Contains(log1, "on the host (private repo)") || !strings.Contains(log1, "turn files: \n") {
		t.Errorf("first turn's log:\n%s", log1)
	}
	// The VM's checkout points at the plain repo URL and holds no credential.
	if !strings.Contains(log1, "origin: "+testRepo+"\n") || !strings.Contains(log1, "credentials: 0") || strings.Contains(log1, "tok-never-in-the-vm") {
		t.Errorf("the VM's checkout:\n%s", log1)
	}
	if err := r.CloseSession(first.ID); err != nil { // saves the warm snapshot
		t.Fatal(err)
	}

	// The next session is rebuilt from the snapshot, updated through the host.
	second := task.New(testRepo, "two")
	second.ThreadID, second.ParentID, second.ThreadBase, second.Fetch = first.ID, first.ID, res1.BaseCommit, fetch
	second.History = func(context.Context) ([]task.Turn, error) {
		return []task.Turn{{Prompt: "one", Reply: res1.Reply, Diff: res1.Diff}}, nil
	}
	_, log2 := runTurn(t, r, second)
	for _, want := range []string{"restored from snapshot", "updated to", "rebuilding the thread's work", "turn files: turn-1.txt"} {
		if !strings.Contains(log2, want) {
			t.Errorf("second turn's log lacks %q:\n%s", want, log2)
		}
	}
}
