package postgres

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// openTest connects to FORGERUN_TEST_DATABASE_URL and empties the tasks
// table. Tests are skipped when the variable is unset.
func openTest(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("FORGERUN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set FORGERUN_TEST_DATABASE_URL to run Postgres tests (see docker-compose.yml)")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.pool.Exec(ctx, `TRUNCATE tasks`); err != nil {
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
	cat := models.NewCatalog("cursor", vs)
	if cat.DefaultFamily() != "auto" {
		t.Errorf("cursor default family = %q, want auto (agents.default_model_id)", cat.DefaultFamily())
	}
	for _, v := range vs {
		got, err := cat.Resolve(models.Choice{Model: v.Family, Effort: v.Effort, Thinking: &v.Thinking, Fast: &v.Fast}, "auto")
		if err != nil || got.ID != v.ID {
			t.Errorf("%s: resolved to %q, %v", v.ID, got.ID, err)
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
		s.pool.Exec(ctx, `DELETE FROM models WHERE agent_id = $1`, id)
		s.pool.Exec(ctx, `DELETE FROM agents WHERE id = $1`, id)
	})
}

func TestModelBelongsToAgent(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	addAgent(t, s, "claude-code")

	tk := task.New("r", "p")
	tk.ModelID = "auto" // Cursor's auto
	if err := s.Create(ctx, tk, "claude-code"); err == nil {
		t.Error("task used another agent's model")
	}
	if err := s.Create(ctx, task.New("r", "p"), "claude-code"); err == nil {
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
	addAgent(t, s, "claude-code")

	if _, err := s.pool.Exec(ctx, `UPDATE agents SET default_model_id = 'auto' WHERE id = 'claude-code'`); err == nil {
		t.Fatal("agent default pointed at another agent's model")
	}

	// With its own model as default, tasks without a model use it.
	if _, err := s.pool.Exec(ctx, `INSERT INTO models (id, agent_id, family, name, is_default) VALUES ('sonnet', 'claude-code', 'sonnet', 'Sonnet', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE agents SET default_model_id = 'sonnet' WHERE id = 'claude-code'`); err != nil {
		t.Fatal(err)
	}
	tk := task.New("r", "p")
	if err := s.Create(ctx, tk, "claude-code"); err != nil {
		t.Fatal(err)
	}
	rec, _ := s.Get(ctx, tk.ID)
	if rec.AgentID != "claude-code" || rec.Model.ID != "sonnet" || !rec.Model.AgentDefault {
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
