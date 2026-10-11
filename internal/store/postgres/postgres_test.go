package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

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
