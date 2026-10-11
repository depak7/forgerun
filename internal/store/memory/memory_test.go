package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

func TestLifecycleAndRules(t *testing.T) {
	s, ctx := New(), context.Background()
	tk := task.New("r", "p")
	if err := s.Create(ctx, tk, "fake"); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, tk.ID, store.Outcome{State: task.Succeeded}); !errors.Is(err, task.ErrInvalidTransition) {
		t.Errorf("Finish on queued: got %v, want ErrInvalidTransition", err)
	}

	rec, ok, err := s.Claim(ctx)
	if err != nil || !ok || rec.ID != tk.ID || rec.State != task.Running {
		t.Fatalf("Claim = %+v, %v, %v", rec, ok, err)
	}
	if _, ok, _ := s.Claim(ctx); ok {
		t.Error("second Claim should find the queue empty")
	}

	if err := s.Finish(ctx, tk.ID, store.Outcome{State: task.Succeeded, PatchPath: "p.patch"}); err != nil {
		t.Fatal(err)
	}
	rec, _ = s.Get(ctx, tk.ID)
	if rec.State != task.Succeeded || rec.PatchPath != "p.patch" || rec.FinishedAt == nil {
		t.Errorf("after Finish: %+v", rec)
	}
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get missing: got %v, want ErrNotFound", err)
	}
}
