package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// cfgDatabase is the database name in url, for error messages.
func cfgDatabase(url string) string {
	if cfg, err := pgx.ParseConfig(url); err == nil {
		return cfg.Database
	}
	return "unparsable URL"
}

// openTest connects to FORGERUN_TEST_DATABASE_URL and empties the tasks
// and snapshots tables. Tests are skipped when the variable is unset.
func openTest(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("FORGERUN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set FORGERUN_TEST_DATABASE_URL to run Postgres tests (see docker-compose.yml)")
	}
	// These tests empty tables. Refuse anything but a test database, so a
	// URL copied from the dev setup can't wipe a running server's data.
	if cfg, err := pgx.ParseConfig(url); err != nil || !strings.HasSuffix(cfg.Database, "_test") {
		t.Fatalf("FORGERUN_TEST_DATABASE_URL must name a test database ending in _test (got %q): these tests empty tables", cfgDatabase(url))
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.pool.Exec(ctx, `TRUNCATE tasks, snapshots`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLifecycle(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	tk := task.New("https://example.com/repo.git", "fix the bug")

	if err := s.Create(ctx, tk, "cursor"); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Queued || rec.Prompt != "fix the bug" || rec.StartedAt != nil {
		t.Fatalf("after Create: %+v", rec)
	}

	if err := s.Start(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, tk.ID, store.Outcome{State: task.Succeeded, LogPath: "out/x/agent.log", PatchPath: "out/x/changes.patch"}); err != nil {
		t.Fatal(err)
	}
	rec, err = s.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != task.Succeeded || rec.PatchPath != "out/x/changes.patch" || rec.Error != "" {
		t.Errorf("after Finish: %+v", rec)
	}
	if rec.StartedAt == nil || rec.FinishedAt == nil || rec.FinishedAt.Before(*rec.StartedAt) {
		t.Errorf("timestamps wrong: started=%v finished=%v", rec.StartedAt, rec.FinishedAt)
	}
}

func TestIllegalTransitions(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	tk := task.New("r", "p")
	if err := s.Create(ctx, tk, "cursor"); err != nil {
		t.Fatal(err)
	}

	// queued → succeeded skips running.
	err := s.Finish(ctx, tk.ID, store.Outcome{State: task.Succeeded})
	if !errors.Is(err, task.ErrInvalidTransition) {
		t.Errorf("Finish on queued task: got %v, want ErrInvalidTransition", err)
	}

	// Starting twice: the second must fail.
	if err := s.Start(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx, tk.ID); !errors.Is(err, task.ErrInvalidTransition) {
		t.Errorf("second Start: got %v, want ErrInvalidTransition", err)
	}
}

func TestNotFound(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get: got %v, want ErrNotFound", err)
	}
	if err := s.Start(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Start: got %v, want ErrNotFound", err)
	}
}

func TestListNewestFirst(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	var ids []string
	for range 3 {
		tk := task.New("r", "p")
		if err := s.Create(ctx, tk, "cursor"); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tk.ID)
		time.Sleep(5 * time.Millisecond) // distinct created_at values
	}

	recs, err := s.List(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].ID != ids[2] || recs[1].ID != ids[1] {
		t.Errorf("List(2) = %v, want [%s %s]", idsOf(recs), ids[2], ids[1])
	}
}

// Two workers race to start the same task; exactly one may win.
func TestStartIsAtomic(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	tk := task.New("r", "p")
	if err := s.Create(ctx, tk, "cursor"); err != nil {
		t.Fatal(err)
	}

	const workers = 10
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.Start(ctx, tk.ID)
		}()
	}
	wg.Wait()

	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, task.ErrInvalidTransition):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("%d workers started the task, want exactly 1", wins)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	openTest(t)
	openTest(t) // second Open re-runs migrate; must be a no-op, not an error
}

func idsOf(recs []store.Record) []string {
	var ids []string
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestClaimOldestFirstAndEmpty(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	if _, ok, err := s.Claim(ctx); err != nil || ok {
		t.Fatalf("Claim on empty queue: ok=%v err=%v", ok, err)
	}

	first, second := task.New("r", "first"), task.New("r", "second")
	for _, tk := range []task.Task{first, second} {
		if err := s.Create(ctx, tk, "cursor"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	rec, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("Claim: ok=%v err=%v", ok, err)
	}
	if rec.ID != first.ID || rec.State != task.Running || rec.StartedAt == nil {
		t.Errorf("Claim returned %+v, want %s running", rec, first.ID)
	}
}

// Many workers claim at once: every task is claimed exactly once.
func TestClaimIsExclusive(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	const tasks, workers = 5, 10
	for range tasks {
		if err := s.Create(ctx, task.New("r", "p"), "cursor"); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu      sync.Mutex
		claimed = map[string]int{}
		wg      sync.WaitGroup
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				rec, ok, err := s.Claim(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if !ok {
					return // queue empty
				}
				mu.Lock()
				claimed[rec.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(claimed) != tasks {
		t.Errorf("%d distinct tasks claimed, want %d", len(claimed), tasks)
	}
	for id, n := range claimed {
		if n != 1 {
			t.Errorf("task %s claimed %d times", id, n)
		}
	}
}

func TestCancel(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	queued, running := task.New("r", "p"), task.New("r", "p")
	for _, tk := range []task.Task{queued, running} {
		if err := s.Create(ctx, tk, "cursor"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Start(ctx, running.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.Cancel(ctx, queued.ID); err != nil {
		t.Fatal(err)
	}
	rec, _ := s.Get(ctx, queued.ID)
	if rec.State != task.Cancelled || rec.FinishedAt == nil {
		t.Errorf("after Cancel: %+v", rec)
	}

	// Running tasks are cancelled by their worker, not by the store.
	if err := s.Cancel(ctx, running.ID); !errors.Is(err, task.ErrInvalidTransition) {
		t.Errorf("Cancel on running task: got %v, want ErrInvalidTransition", err)
	}
}

func TestFailRunning(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	queued, running := task.New("r", "p"), task.New("r", "p")
	for _, tk := range []task.Task{queued, running} {
		if err := s.Create(ctx, tk, "cursor"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Start(ctx, running.ID); err != nil {
		t.Fatal(err)
	}

	n, err := s.FailRunning(ctx, "server restarted")
	if err != nil || n != 1 {
		t.Fatalf("FailRunning = %d, %v; want 1, nil", n, err)
	}
	rec, _ := s.Get(ctx, running.ID)
	if rec.State != task.Failed || rec.Error != "server restarted" {
		t.Errorf("running task after FailRunning: %+v", rec)
	}
	rec, _ = s.Get(ctx, queued.ID)
	if rec.State != task.Queued {
		t.Errorf("queued task must be untouched, got %s", rec.State)
	}
}

// The real seed: every enabled model must be reachable by its own options.
func TestSeededModelsAreReachable(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	vs, err := s.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) < 200 {
		t.Fatalf("only %d models; is the seed migration applied?", len(vs))
	}
	wantDefault := map[string]string{"cursor": "auto", "claude-code": "sonnet"} // agents.default_model_id
	for agentID, want := range wantDefault {
		cat := models.NewCatalog(agentID, vs)
		if cat.DefaultFamily() != want {
			t.Errorf("%s default family = %q, want %q", agentID, cat.DefaultFamily(), want)
		}
	}
	// Every seeded variant can be chosen through its own agent's catalog.
	for _, v := range vs {
		cat := models.NewCatalog(v.AgentID, vs)
		got, err := cat.Resolve(models.Choice{Model: v.Family, Effort: v.Effort, Thinking: &v.Thinking, Fast: &v.Fast}, "")
		if err != nil || got.ID != v.ID {
			t.Errorf("%s/%s: resolved to %q, %v", v.AgentID, v.ID, got.ID, err)
		}
	}
}

func TestTaskCarriesModel(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	tk := task.New("r", "p")
	tk.ModelID = "claude-opus-5-5-high-fast"
	if err := s.Create(ctx, tk, "cursor"); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := s.Claim(ctx) // Claim's UPDATE … FROM must return the model too
	if err != nil || !ok {
		t.Fatalf("Claim: %v %v", ok, err)
	}
	m := rec.Model
	if m.ID != tk.ModelID || m.Family != "claude-opus-5-5" || m.Effort != "high" || !m.Fast || m.Thinking {
		t.Errorf("model = %+v", m)
	}

	plain := task.New("r", "p") // no model → auto
	if err := s.Create(ctx, plain, "cursor"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Get(ctx, plain.ID); rec.Model.ID != "auto" {
		t.Errorf("default model = %q, want auto", rec.Model.ID)
	}
}

func TestCreateRejectsUnknownModel(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	tk := task.New("r", "p")
	tk.ModelID = "no-such-model"
	if err := s.Create(ctx, tk, "cursor"); err == nil {
		t.Fatal("Create accepted an unknown model")
	}
}

// Disabling a model hides it from choices but not from tasks that used it.
func TestDisabledModelStillShownOnTasks(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	const id = "gpt-5.2-high"
	tk := task.New("r", "p")
	tk.ModelID = id
	if err := s.Create(ctx, tk, "cursor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE models SET enabled = false WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.pool.Exec(context.Background(), `UPDATE models SET enabled = true WHERE id = $1`, id) })

	vs, _ := s.ListModels(ctx)
	if slices.ContainsFunc(vs, func(v models.Variant) bool { return v.ID == id }) {
		t.Error("disabled model still listed")
	}
	if rec, _ := s.Get(ctx, tk.ID); rec.Model.ID != id {
		t.Errorf("task's model = %q, want %s", rec.Model.ID, id)
	}
}

// addAgent inserts a test agent and removes it, with its rows, afterwards.
func addAgent(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO agents (id, name) VALUES ($1, $1)`, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		s.pool.Exec(ctx, `UPDATE agents SET default_model_id = NULL WHERE id = $1`, id)
		s.pool.Exec(ctx, `DELETE FROM tasks WHERE agent_id = $1`, id)
		s.pool.Exec(ctx, `DELETE FROM snapshots WHERE agent_id = $1`, id)
		s.pool.Exec(ctx, `DELETE FROM models WHERE agent_id = $1`, id)
		s.pool.Exec(ctx, `DELETE FROM agents WHERE id = $1`, id)
	})
}

func TestModelBelongsToAgent(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	addAgent(t, s, "test-agent")

	tk := task.New("r", "p")
	tk.ModelID = "auto" // Cursor's auto
	if err := s.Create(ctx, tk, "test-agent"); err == nil {
		t.Error("task used another agent's model")
	}
	if err := s.Create(ctx, task.New("r", "p"), "test-agent"); err == nil {
		t.Error("task created without a model for an agent with no default")
	}
	if err := s.Create(ctx, task.New("r", "p"), "no-such-agent"); err == nil {
		t.Error("task created for an unknown agent")
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO models (id, agent_id, family, name) VALUES ('x', 'no-such-agent', 'x', 'x')`); err == nil {
		t.Error("model added for an unknown agent")
	}
}

func TestAgentDefaultMustBeOwnModel(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	addAgent(t, s, "test-agent")

	if _, err := s.pool.Exec(ctx, `UPDATE agents SET default_model_id = 'auto' WHERE id = 'test-agent'`); err == nil {
		t.Fatal("agent default pointed at another agent's model")
	}

	// With its own model as default, tasks without a model use it.
	if _, err := s.pool.Exec(ctx, `INSERT INTO models (id, agent_id, family, name, is_default) VALUES ('sonnet', 'test-agent', 'sonnet', 'Sonnet', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE agents SET default_model_id = 'sonnet' WHERE id = 'test-agent'`); err != nil {
		t.Fatal(err)
	}
	tk := task.New("r", "p")
	if err := s.Create(ctx, tk, "test-agent"); err != nil {
		t.Fatal(err)
	}
	rec, _ := s.Get(ctx, tk.ID)
	if rec.AgentID != "test-agent" || rec.Model.ID != "sonnet" || !rec.Model.AgentDefault {
		t.Errorf("task = agent %q model %+v", rec.AgentID, rec.Model)
	}
}

func TestGetAgent(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	a, err := s.GetAgent(ctx, "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "Cursor" || !a.Enabled || a.DefaultModelID != "auto" {
		t.Errorf("cursor = %+v", a)
	}
	if _, err := s.GetAgent(ctx, "nope"); !errors.Is(err, store.ErrAgentNotFound) {
		t.Errorf("unknown agent: got %v, want ErrAgentNotFound", err)
	}
}

func TestDisabledAgentHidesModels(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE agents SET enabled = false WHERE id = 'cursor'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.pool.Exec(context.Background(), `UPDATE agents SET enabled = true WHERE id = 'cursor'`) })

	vs, err := s.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(vs, func(v models.Variant) bool { return v.AgentID == "cursor" }) {
		t.Error("models of a disabled agent are still listed")
	}
}

func TestListAgents(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	addAgent(t, s, "test-agent")
	if _, err := s.pool.Exec(ctx, `UPDATE agents SET enabled = false WHERE id = 'test-agent'`); err != nil {
		t.Fatal(err)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(agents, func(a store.Agent) bool { return a.ID == "cursor" && a.DefaultModelID == "auto" }) {
		t.Errorf("cursor missing from %+v", agents)
	}
	if slices.ContainsFunc(agents, func(a store.Agent) bool { return a.ID == "test-agent" }) {
		t.Error("disabled agent listed")
	}
}

// newSnap is a snapshot to save for repo and agent.
func newSnap(repo, agent, ref string) store.Snapshot {
	return store.Snapshot{Repo: repo, AgentID: agent, Ref: ref, Commit: "abc123", DepsFingerprint: "fp", SizeBytes: 4096}
}

func TestReplaceSnapshot(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	const repo = "https://github.com/x/y"

	if _, err := s.CurrentSnapshot(ctx, repo, "cursor"); !errors.Is(err, store.ErrSnapshotNotFound) {
		t.Fatalf("before any snapshot: got %v, want ErrSnapshotNotFound", err)
	}

	first, retired, err := s.ReplaceSnapshot(ctx, newSnap(repo, "cursor", "/snaps/1"))
	if err != nil {
		t.Fatal(err)
	}
	if retired != nil {
		t.Errorf("first snapshot retired %+v", *retired)
	}
	if first.ID == 0 || first.State != store.SnapshotCurrent || first.Ref != "/snaps/1" || first.Commit != "abc123" ||
		first.DepsFingerprint != "fp" || first.SizeBytes != 4096 || first.CreatedAt.IsZero() || first.DeletedAt != nil {
		t.Errorf("saved = %+v", first)
	}

	second, retired, err := s.ReplaceSnapshot(ctx, newSnap(repo, "cursor", "/snaps/2"))
	if err != nil {
		t.Fatal(err)
	}
	if retired == nil || retired.ID != first.ID || retired.State != store.SnapshotRetired {
		t.Fatalf("second replace retired %+v, want snapshot %d as retired", retired, first.ID)
	}
	if cur, err := s.CurrentSnapshot(ctx, repo, "cursor"); err != nil || cur.ID != second.ID {
		t.Errorf("current = %d (%v), want %d", cur.ID, err, second.ID)
	}

	// The retired one is still listed (still on disk) until marked deleted.
	if snaps, _ := s.ListSnapshots(ctx); len(snaps) != 2 {
		t.Errorf("listed %d snapshots, want 2", len(snaps))
	}
	for range 2 { // marking twice is fine
		if err := s.MarkSnapshotDeleted(ctx, first.ID); err != nil {
			t.Fatal(err)
		}
	}
	snaps, _ := s.ListSnapshots(ctx)
	if len(snaps) != 1 || snaps[0].ID != second.ID {
		t.Errorf("after delete, listed %+v, want only %d", snaps, second.ID)
	}
	if err := s.MarkSnapshotDeleted(ctx, 999999); !errors.Is(err, store.ErrSnapshotNotFound) {
		t.Errorf("unknown snapshot: got %v, want ErrSnapshotNotFound", err)
	}
}

func TestSnapshotsPerRepoAndAgent(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	addAgent(t, s, "test-agent")

	c, _, err := s.ReplaceSnapshot(ctx, newSnap("https://GitHub.com/x/y.git", "cursor", "/c"))
	if err != nil {
		t.Fatal(err)
	}
	// Another agent on the same repo gets its own snapshot; cursor's stays current.
	if _, retired, err := s.ReplaceSnapshot(ctx, newSnap("https://github.com/x/y", "test-agent", "/cc")); err != nil || retired != nil {
		t.Fatalf("test-agent snapshot: retired %+v, err %v", retired, err)
	}
	// Different spellings of the repo find the same snapshot.
	for _, repo := range []string{"https://github.com/x/y", "https://github.com/x/y/", "https://GITHUB.com/x/y.git"} {
		if cur, err := s.CurrentSnapshot(ctx, repo, "cursor"); err != nil || cur.ID != c.ID {
			t.Errorf("CurrentSnapshot(%q) = %d (%v), want %d", repo, cur.ID, err, c.ID)
		}
	}
	if c.Repo != "https://github.com/x/y" {
		t.Errorf("stored repo = %q, want normalized", c.Repo)
	}
	if _, err := s.CurrentSnapshot(ctx, "https://github.com/x/other", "cursor"); !errors.Is(err, store.ErrSnapshotNotFound) {
		t.Errorf("other repo: got %v, want ErrSnapshotNotFound", err)
	}
	if _, _, err := s.ReplaceSnapshot(ctx, newSnap("https://github.com/x/y", "no-such-agent", "/n")); err == nil {
		t.Error("snapshot saved for an unknown agent")
	}
}

func TestUseSnapshot(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	a, _, _ := s.ReplaceSnapshot(ctx, newSnap("https://github.com/x/a", "cursor", "/a"))
	b, _, _ := s.ReplaceSnapshot(ctx, newSnap("https://github.com/x/b", "cursor", "/b"))
	tk := task.New("https://github.com/x/a", "p")
	if err := s.Create(ctx, tk, "cursor"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Get(ctx, tk.ID); rec.SnapshotID != nil {
		t.Errorf("new task has snapshot %d, want nil (cold)", *rec.SnapshotID)
	}

	// Using a makes it the most recently used: b is listed first now.
	if err := s.UseSnapshot(ctx, tk.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Get(ctx, tk.ID); rec.SnapshotID == nil || *rec.SnapshotID != a.ID {
		t.Errorf("task snapshot = %v, want %d", rec.SnapshotID, a.ID)
	}
	snaps, _ := s.ListSnapshots(ctx)
	if len(snaps) != 2 || snaps[0].ID != b.ID || !snaps[1].LastUsedAt.After(a.LastUsedAt) {
		t.Errorf("listed %+v, want b then a (a used later)", snaps)
	}

	if err := s.UseSnapshot(ctx, "no-such-task", a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown task: got %v, want ErrNotFound", err)
	}
	s.MarkSnapshotDeleted(ctx, b.ID)
	if err := s.UseSnapshot(ctx, tk.ID, b.ID); !errors.Is(err, store.ErrSnapshotNotFound) {
		t.Errorf("deleted snapshot: got %v, want ErrSnapshotNotFound", err)
	}
}

// Many tasks for one repo finishing at once still leave exactly one current
// snapshot, and every other one is retired exactly once (so it gets deleted).
func TestReplaceSnapshotIsAtomic(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	const n = 10
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		retired = map[int64]int{}
	)
	for i := range n {
		wg.Go(func() {
			_, old, err := s.ReplaceSnapshot(ctx, newSnap("https://github.com/x/y", "cursor", fmt.Sprintf("/s%d", i)))
			if err != nil {
				t.Error(err)
				return
			}
			if old != nil {
				mu.Lock()
				retired[old.ID]++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	cur, err := s.CurrentSnapshot(ctx, "https://github.com/x/y", "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if len(retired) != n-1 {
		t.Errorf("%d snapshots retired, want %d", len(retired), n-1)
	}
	for id, times := range retired {
		if times != 1 || id == cur.ID {
			t.Errorf("snapshot %d retired %d times (current is %d)", id, times, cur.ID)
		}
	}
}

func TestPullRequestFields(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	tk := task.New("https://github.com/o/r", "p")
	tk.PR = true
	if err := s.Create(ctx, tk, "cursor"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Get(ctx, tk.ID); !rec.PR.Requested || rec.PR.URL != "" || rec.Summary != "" || rec.BaseCommit != "" {
		t.Fatalf("after Create: %+v", rec)
	}
	s.Start(ctx, tk.ID)
	o := store.Outcome{State: task.Succeeded, BaseCommit: "abc123", Summary: "## Requested\nx",
		PRURL: "https://github.com/o/r/pull/1"}
	if err := s.Finish(ctx, tk.ID, o); err != nil {
		t.Fatal(err)
	}
	rec, _ := s.Get(ctx, tk.ID)
	if rec.PR != (store.PullRequest{Requested: true, URL: "https://github.com/o/r/pull/1"}) ||
		rec.BaseCommit != "abc123" || rec.Summary != "## Requested\nx" {
		t.Errorf("after Finish: pr=%+v base=%q summary=%q", rec.PR, rec.BaseCommit, rec.Summary)
	}
	other := task.New("r", "p") // no PR asked
	s.Create(ctx, other, "cursor")
	if rec, _ := s.Get(ctx, other.ID); rec.PR.Requested {
		t.Error("PR requested without asking")
	}
}

// prTask creates a task that finished with an opened pull request.
func prTask(t *testing.T, s *Store, parent string) task.Task {
	t.Helper()
	ctx := context.Background()
	tk := task.New("https://github.com/o/r", "p")
	tk.PR, tk.ParentID = true, parent
	if err := s.Create(ctx, tk, "cursor"); err != nil {
		t.Fatal(err)
	}
	return tk
}

func finishTask(t *testing.T, s *Store, id string, o store.Outcome) {
	t.Helper()
	ctx := context.Background()
	if err := s.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, id, o); err != nil {
		t.Fatal(err)
	}
}

func TestThreads(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	first := prTask(t, s, "")
	finishTask(t, s, first.ID, store.Outcome{State: task.Succeeded, Summary: "s1",
		PRURL: "https://github.com/o/r/pull/9", PRNumber: 9, PRBranch: "forgerun/" + first.ID})

	second := prTask(t, s, first.ID)
	rec, _ := s.Get(ctx, second.ID)
	if rec.ParentID != first.ID || rec.ThreadID != first.ID || !rec.PR.Requested ||
		rec.PR.Number != 9 || rec.PR.Branch != "forgerun/"+first.ID || rec.PR.URL != "https://github.com/o/r/pull/9" {
		t.Fatalf("follow-up = %+v", rec)
	}
	if root, _ := s.Get(ctx, first.ID); root.ThreadID != first.ID || root.ParentID != "" {
		t.Errorf("first task: thread %q parent %q", root.ThreadID, root.ParentID)
	}

	// One task at a time per thread; one follow-up per task.
	if err := s.Create(ctx, withParent(second.ID), "cursor"); !errors.Is(err, store.ErrThreadBusy) {
		t.Errorf("busy thread: got %v, want ErrThreadBusy", err)
	}
	// A follow-up that fails without publishing keeps the thread's pull request.
	finishTask(t, s, second.ID, store.Outcome{State: task.Failed, Error: "agent failed"})
	if rec, _ := s.Get(ctx, second.ID); rec.PR.Number != 9 || rec.PR.URL == "" {
		t.Errorf("failed follow-up lost the PR: %+v", rec.PR)
	}
	if err := s.Create(ctx, withParent(first.ID), "cursor"); !errors.Is(err, store.ErrNotLatest) {
		t.Errorf("second follow-up on one task: got %v, want ErrNotLatest", err)
	}
	if err := s.Create(ctx, withParent("t-nope"), "cursor"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown parent: got %v, want ErrNotFound", err)
	}

	third := prTask(t, s, second.ID)
	thread, err := s.ThreadTasks(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ids := idsOf(thread); !slices.Equal(ids, []string{first.ID, second.ID, third.ID}) {
		t.Errorf("thread = %v", ids)
	}
}

// Two follow-ups on the same task at the same moment: exactly one is created.
func TestFollowUpRace(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	first := prTask(t, s, "")
	finishTask(t, s, first.ID, store.Outcome{State: task.Succeeded, PRNumber: 9, PRBranch: "b", PRURL: "u"})
	for range 5 {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		parent := first.ID
		if thread, _ := s.ThreadTasks(ctx, first.ID); len(thread) > 1 {
			parent = thread[len(thread)-1].ID
			finishTask(t, s, parent, store.Outcome{State: task.Succeeded})
		}
		for i := range errs {
			wg.Go(func() { errs[i] = s.Create(ctx, withParent(parent), "cursor") })
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrNotLatest), errors.Is(err, store.ErrThreadBusy):
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}
		if ok != 1 {
			t.Fatalf("%d of 2 concurrent follow-ups were created, want exactly 1 (%v)", ok, errs)
		}
	}
}

func withParent(id string) task.Task {
	tk := task.New("https://github.com/o/r", "p")
	tk.PR, tk.ParentID = true, id
	return tk
}

func TestListThreadsAndCounts(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	addAgent(t, s, "test-agent")
	if _, err := s.pool.Exec(ctx, `INSERT INTO models (id, agent_id, family, name, is_default) VALUES ('m', 'test-agent', 'm', 'M', true)`); err != nil {
		t.Fatal(err)
	}
	s.pool.Exec(ctx, `UPDATE agents SET default_model_id = 'm' WHERE id = 'test-agent'`)

	// Thread A: a cursor task with a PR, then a follow-up on test-agent that failed.
	a := prTask(t, s, "")
	finishTask(t, s, a.ID, store.Outcome{State: task.Succeeded, PRURL: "u", PRNumber: 5, PRBranch: "b"})
	a2 := task.New("https://github.com/o/r", "follow-up")
	a2.ParentID = a.ID
	if err := s.Create(ctx, a2, "test-agent"); err != nil {
		t.Fatal(err)
	}
	finishTask(t, s, a2.ID, store.Outcome{State: task.Failed, Error: "agent stalled"})
	// Thread B: a single task, still queued, created last.
	b := task.New("https://github.com/o/other", "on its own")
	if err := s.Create(ctx, b, "cursor"); err != nil {
		t.Fatal(err)
	}

	threads, err := s.ListThreads(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 {
		t.Fatalf("%d threads, want 2", len(threads))
	}
	// Threads come most recent activity first; look them up by ID here.
	byID := map[string]store.Thread{threads[0].ID: threads[0], threads[1].ID: threads[1]}
	ta := byID[a.ID]
	if ta.Title != a.Prompt || ta.Steps != 2 || !slices.Equal(ta.Agents, []string{"cursor", "test-agent"}) ||
		ta.Latest.ID != a2.ID || ta.Latest.State != task.Failed || ta.Latest.PR.Number != 5 {
		t.Errorf("thread A = %+v", ta)
	}
	if tb := byID[b.ID]; tb.Steps != 1 || tb.Latest.ID != b.ID || tb.Latest.State != task.Queued {
		t.Errorf("thread B = %+v", tb)
	}
	if !threads[0].UpdatedAt.After(threads[1].UpdatedAt) && !threads[0].UpdatedAt.Equal(threads[1].UpdatedAt) {
		t.Errorf("not newest first: %v then %v", threads[0].UpdatedAt, threads[1].UpdatedAt)
	}
	if one, _ := s.ListThreads(ctx, 1); len(one) != 1 || one[0].ID != threads[0].ID {
		t.Errorf("limit 1 = %+v", one)
	}

	counts, err := s.CountTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts[task.Succeeded] != 1 || counts[task.Failed] != 1 || counts[task.Queued] != 1 || counts[task.Running] != 0 {
		t.Errorf("counts = %v", counts)
	}
}
