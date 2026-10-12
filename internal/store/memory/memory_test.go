package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

var fakeDefault = models.Variant{ID: "auto", AgentID: "fake", Family: "auto", IsDefault: true, AgentDefault: true, ZDR: true}

func TestLifecycleAndRules(t *testing.T) {
	s, ctx := New(fakeDefault), context.Background()
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

func TestModels(t *testing.T) {
	v := models.Variant{ID: "m-high", AgentID: "fake", Family: "m", Effort: "high", IsDefault: true, ZDR: true}
	s, ctx := New(fakeDefault, v), context.Background()

	tk := task.New("r", "p")
	tk.ModelID = "m-high"
	if err := s.Create(ctx, tk, "fake"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Get(ctx, tk.ID); rec.Model.ID != "m-high" {
		t.Errorf("model = %+v", rec.Model)
	}
	bad := task.New("r", "p")
	bad.ModelID = "nope"
	if err := s.Create(ctx, bad, "fake"); err == nil {
		t.Error("unknown model accepted")
	}
	if err := s.Create(ctx, task.New("r", "p"), "other-agent"); err == nil {
		t.Error("task created for an agent with no default model")
	}
	plain := task.New("r", "p")
	if err := s.Create(ctx, plain, "fake"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Get(ctx, plain.ID); rec.Model.ID != "auto" {
		t.Errorf("no model: got %q, want the agent default auto", rec.Model.ID)
	}
	vs, _ := s.ListModels(ctx)
	if len(vs) != 2 || vs[0].ID != "auto" || vs[1].ID != "m-high" {
		t.Errorf("ListModels = %+v", vs)
	}
}

func TestGetAgent(t *testing.T) {
	s, ctx := New(fakeDefault), context.Background()
	a, err := s.GetAgent(ctx, "fake")
	if err != nil || !a.Enabled || a.DefaultModelID != "auto" {
		t.Errorf("fake = %+v, %v", a, err)
	}
	if _, err := s.GetAgent(ctx, "nope"); !errors.Is(err, store.ErrAgentNotFound) {
		t.Errorf("unknown agent: got %v, want ErrAgentNotFound", err)
	}
}
