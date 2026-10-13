package snapshots

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
)

const testRepo = "https://github.com/octocat/Hello-World" // tiny; default branch "master"

// messyAgent reports what it started with, then leaves behind everything the cleanup must deal with: an edit
// it commits on a new branch, an untracked file, an ignored cache (kept),
// and state in its own home folder. If fail is set, it then fails.
type messyAgent struct{ fail bool }

func (messyAgent) ID() string                { return "fake" }
func (messyAgent) Image() string             { return "depak7/forgerun-agent-cursor:dev" }
func (messyAgent) Secrets() []sandbox.Secret { return nil }
func (messyAgent) StatePaths() []string      { return []string{"/root/.fake-agent"} }
func (a messyAgent) Run(ctx context.Context, sb *sandbox.Sandbox, req agent.Request, logs io.Writer) error {
	// The first two lines report what the task started with, for the tests.
	script := `set -e
echo "agent sees commit $(git rev-parse HEAD)"
if [ -e ignored-cache/data ]; then echo "agent found the ignored cache"; fi
echo edited-by-agent >> README
git checkout -q -b agent-branch
git -c user.email=a@example.com -c user.name=agent commit -qam "agent commit"
echo junk > untracked.txt
echo ignored-cache/ >> .git/info/exclude
mkdir -p ignored-cache && echo kept > ignored-cache/data
mkdir -p /root/.fake-agent && echo chat > /root/.fake-agent/history`
	if err := sb.Stream(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", script}, Dir: req.Dir}, logs); err != nil {
		return err
	}
	if a.fail {
		return errors.New("agent failed on purpose")
	}
	return nil
}

func setup(t *testing.T, a agent.Agent) (*task.Runner, *memory.Store) {
	t.Helper()
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot real VMs")
	}
	s := memory.New(models.Variant{ID: "auto", AgentID: "fake", Family: "auto", AgentDefault: true})
	t.Cleanup(func() { // whatever the test left behind
		snaps, _ := s.ListSnapshots(context.Background())
		for _, snap := range snaps {
			sandbox.DeleteSnapshot(context.Background(), snap.Ref)
		}
	})
	r := &task.Runner{Agent: a, OutDir: t.TempDir(), CPUs: 1, MemMiB: 1024, Timeout: 5 * time.Minute,
		Stdout: io.Discard, Snapshots: &Manager{Store: s}}
	return r, s
}

func TestSaveCleansAndReplaces(t *testing.T) {
	r, s := setup(t, messyAgent{})
	ctx := context.Background()

	// Task 1: its VM becomes the repo's snapshot.
	res, err := r.Run(ctx, task.New(testRepo, "unused"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CurrentSnapshot(ctx, testRepo, "fake")
	if err != nil {
		log, _ := os.ReadFile(res.LogPath)
		t.Fatalf("no snapshot after a successful task: %v\nlog:\n%s", err, log)
	}
	if log, _ := os.ReadFile(res.LogPath); !strings.Contains(string(log), "saved at") {
		t.Errorf("log doesn't mention the snapshot:\n%s", log)
	}

	// The snapshot looks like a fresh clone of master, plus the ignored cache.
	sb, err := sandbox.Restore(ctx, first.Ref, "snapcheck-"+time.Now().Format("150405"),
		sandbox.Config{CPUs: 1, MemoryMiB: 512, MaxLifetime: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Destroy()
	sh := func(script string) string {
		t.Helper()
		out, err := sb.Run(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", script}, Dir: "/work/repo"})
		if err != nil {
			t.Fatalf("%s: %v", script, err)
		}
		return strings.TrimSpace(out)
	}
	checks := []struct{ name, script, want string }{
		{"branch", "git rev-parse --abbrev-ref HEAD", "master"},
		{"commit", "git rev-parse HEAD", first.Commit},
		{"agent edit undone", "grep -c edited-by-agent README || true", "0"},
		{"untracked removed", "test -e untracked.txt && echo present || echo gone", "gone"},
		{"working tree clean", "git status --porcelain", ""},
		{"agent branch deleted", "git branch --format='%(refname:short)'", "master"},
		{"ignored cache kept", "cat ignored-cache/data", "kept"},
		{"agent state wiped", "test -e /root/.fake-agent && echo present || echo gone", "gone"},
	}
	for _, c := range checks {
		if got := sh(c.script); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// Task 2 replaces it; the first is deleted from disk and in the store.
	res, err = r.Run(ctx, task.New(testRepo+".git", "unused")) // same repo, other spelling
	if err != nil {
		t.Fatal(err)
	}
	log2, _ := os.ReadFile(res.LogPath)
	second, err := s.CurrentSnapshot(ctx, testRepo, "fake")
	if err != nil || second.ID == first.ID {
		t.Fatalf("current after task 2 = %d (%v), want a new one", second.ID, err)
	}
	snaps, _ := s.ListSnapshots(ctx)
	if len(snaps) != 1 {
		t.Errorf("%d snapshots remain, want 1: %+v\ntask 2 log:\n%s", len(snaps), snaps, log2)
	}
	if _, err := os.Stat(first.Ref); !os.IsNotExist(err) {
		t.Errorf("first snapshot still on disk at %s (stat: %v)", first.Ref, err)
	}
}

func TestNoSnapshotAfterFailure(t *testing.T) {
	r, s := setup(t, messyAgent{fail: true})
	if _, err := r.Run(context.Background(), task.New(testRepo, "unused")); err == nil {
		t.Fatal("task should have failed")
	}
	if snaps, _ := s.ListSnapshots(context.Background()); len(snaps) != 0 {
		t.Errorf("failed task left snapshots: %+v", snaps)
	}
}

// A store that can't save must not fail the task, nor leave the snapshot
// it took on disk.
func TestSaveFailureIsOnlyAWarning(t *testing.T) {
	r, s := setup(t, messyAgent{})
	failing := &failingStore{Store: s}
	r.Snapshots = &Manager{Store: failing}

	res, err := r.Run(context.Background(), task.New(testRepo, "unused"))
	if err != nil {
		t.Fatalf("task failed because of the snapshot: %v", err)
	}
	if log, _ := os.ReadFile(res.LogPath); !strings.Contains(string(log), "warning: snapshot not saved") {
		t.Errorf("log has no warning:\n%s", log)
	}
	if failing.ref == "" {
		t.Fatal("the snapshot step never reached the store")
	}
	if _, err := os.Stat(failing.ref); !os.IsNotExist(err) {
		t.Errorf("unrecorded snapshot left on disk at %s", failing.ref)
	}
}

// failingStore embeds a real store and overrides one method to fail.
type failingStore struct {
	store.Store
	ref string // the snapshot it refused
}

func (f *failingStore) ReplaceSnapshot(_ context.Context, snap store.Snapshot) (store.Snapshot, *store.Snapshot, error) {
	f.ref = snap.Ref
	return store.Snapshot{}, nil, errors.New("database is down")
}

func TestGroup(t *testing.T) {
	g := Group("https://github.com/octocat/Hello-World", "cursor")
	if !strings.HasPrefix(g, "forgerun-cursor-hello-world-") || len(g) != len("forgerun-cursor-hello-world-")+12 {
		t.Errorf("Group = %q", g)
	}
	if Group("https://GitHub.com/octocat/Hello-World.git/", "cursor") != g {
		t.Error("spellings of one repo got different groups")
	}
	for _, other := range []string{
		Group("https://github.com/octocat/Hello-World", "claude-code"), // other agent
		Group("https://github.com/someone/Hello-World", "cursor"),      // same name, other owner
	} {
		if other == g {
			t.Errorf("%q collides with %q", other, g)
		}
	}
	if got := slug("__Hello_World.js--"); got != "hello-world-js" {
		t.Errorf("slug = %q", got)
	}
}

// A second task on the same repo starts from the first one's snapshot and
// inherits its ignored files (stand-ins for installed dependencies).
func TestWarmStart(t *testing.T) {
	r, s := setup(t, messyAgent{})
	ctx := context.Background()

	run := func() (store.Record, string) {
		t.Helper()
		tk := task.New(testRepo, "unused")
		if err := s.Create(ctx, tk, "fake"); err != nil { // UseSnapshot records against it
			t.Fatal(err)
		}
		res, err := r.Run(ctx, tk)
		if err != nil {
			t.Fatal(err)
		}
		rec, _ := s.Get(ctx, tk.ID)
		log, _ := os.ReadFile(res.LogPath)
		return rec, string(log)
	}

	cold, coldLog := run()
	if cold.SnapshotID != nil || !strings.Contains(coldLog, "starting cold") || strings.Contains(coldLog, "found the ignored cache") {
		t.Fatalf("first task should start cold with nothing inherited (snapshot %v):\n%s", cold.SnapshotID, coldLog)
	}
	snap, _ := s.CurrentSnapshot(ctx, testRepo, "fake")

	warm, warmLog := run()
	if warm.SnapshotID == nil || *warm.SnapshotID != snap.ID {
		t.Errorf("second task's snapshot = %v, want %d", warm.SnapshotID, snap.ID)
	}
	for _, want := range []string{"restored from snapshot", "(warm)", "agent found the ignored cache", "agent sees commit " + snap.Commit} {
		if !strings.Contains(warmLog, want) {
			t.Errorf("second task's log lacks %q:\n%s", want, warmLog)
		}
	}
	t.Logf("cold: %s", readyLine(coldLog))
	t.Logf("warm: %s", readyLine(warmLog))
}

// A snapshot taken at an old commit is brought up to date before the agent runs.
func TestWarmStartUpdatesCode(t *testing.T) {
	r, s := setup(t, messyAgent{})
	ctx := context.Background()

	// Build an outdated snapshot by hand: a checkout one commit behind, with
	// origin/master also pointing there, as if the snapshot were old.
	vm, err := sandbox.New(ctx, "outdated-"+time.Now().Format("150405"), sandbox.Config{
		Image: "depak7/forgerun-agent-cursor:dev", CPUs: 1, MemoryMiB: 512, MaxLifetime: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Destroy()
	out, err := vm.Run(ctx, sandbox.Cmd{Name: "sh", Args: []string{"-c", `set -e
git clone -q --depth=2 ` + testRepo + ` /work/repo && cd /work/repo
git rev-parse HEAD
git update-ref refs/remotes/origin/master HEAD~1
git reset -q --hard HEAD~1
git rev-parse HEAD`}})
	if err != nil {
		t.Fatal(err)
	}
	commits := strings.Fields(out) // [latest, older]
	latest, older := commits[0], commits[1]
	img, err := vm.TakeSnapshot(ctx, Group(testRepo, "fake"), "outdated-"+time.Now().Format("150405"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReplaceSnapshot(ctx, store.Snapshot{Repo: testRepo, AgentID: "fake", Ref: img.Ref, Commit: older}); err != nil {
		t.Fatal(err)
	}

	res, err := r.Run(ctx, task.New(testRepo, "unused"))
	if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(res.LogPath)
	if !strings.Contains(string(log), "agent sees commit "+latest) {
		t.Errorf("agent didn't get the latest commit %.7s (snapshot was at %.7s):\n%s", latest, older, log)
	}
}

// A snapshot whose files are gone: the task still succeeds from a cold start,
// and the broken snapshot is removed so the next task doesn't retry it.
func TestBrokenSnapshotFallsBackCold(t *testing.T) {
	r, s := setup(t, messyAgent{})
	ctx := context.Background()
	broken, _, err := s.ReplaceSnapshot(ctx, store.Snapshot{Repo: testRepo, AgentID: "fake",
		Ref: "/nonexistent/forgerun-test/snap_missing", Commit: "abc"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.Run(ctx, task.New(testRepo, "unused"))
	if err != nil {
		t.Fatalf("task failed because of a broken snapshot: %v", err)
	}
	log, _ := os.ReadFile(res.LogPath)
	for _, want := range []string{"warning: snapshot not usable, starting cold", "(cold)"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	snaps, _ := s.ListSnapshots(ctx)
	for _, snap := range snaps {
		if snap.ID == broken.ID {
			t.Errorf("broken snapshot %d is still listed (%s); log:\n%s", snap.ID, snap.State, log)
		}
	}
}

// readyLine picks the "sandbox ready in …" line out of a task log.
func readyLine(log string) string {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "sandbox ready in") {
			return line
		}
	}
	return "(no ready line)"
}
