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

func TestListAgents(t *testing.T) {
	other := models.Variant{ID: "x", AgentID: "another", Family: "x", IsDefault: true}
	s := New(fakeDefault, other)
	agents, err := s.ListAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].ID != "another" || agents[1].ID != "fake" || agents[1].DefaultModelID != "auto" {
		t.Errorf("agents = %+v", agents)
	}
}

// The same rules as the Postgres snapshot tests, in one pass.
func TestSnapshots(t *testing.T) {
	other := models.Variant{ID: "m", AgentID: "other", Family: "m", AgentDefault: true}
	s, ctx := New(fakeDefault, other), context.Background()
	snap := func(repo, agent, ref string) store.Snapshot {
		return store.Snapshot{Repo: repo, AgentID: agent, Ref: ref, Commit: "abc"}
	}

	if _, err := s.CurrentSnapshot(ctx, "https://h/x/y", "fake"); !errors.Is(err, store.ErrSnapshotNotFound) {
		t.Fatalf("before any snapshot: got %v", err)
	}
	first, retired, err := s.ReplaceSnapshot(ctx, snap("https://H/x/y.git", "fake", "/1"))
	if err != nil || retired != nil || first.State != store.SnapshotCurrent || first.Repo != "https://h/x/y" {
		t.Fatalf("first = %+v, retired %v, err %v", first, retired, err)
	}
	if _, retired, _ := s.ReplaceSnapshot(ctx, snap("https://h/x/y", "other", "/o")); retired != nil {
		t.Error("another agent's snapshot retired fake's")
	}
	second, retired, _ := s.ReplaceSnapshot(ctx, snap("https://h/x/y/", "fake", "/2"))
	if retired == nil || retired.ID != first.ID || retired.State != store.SnapshotRetired {
		t.Fatalf("retired %+v, want %d", retired, first.ID)
	}
	if cur, _ := s.CurrentSnapshot(ctx, "https://h/x/y", "fake"); cur.ID != second.ID {
		t.Errorf("current = %d, want %d", cur.ID, second.ID)
	}
	if _, _, err := s.ReplaceSnapshot(ctx, snap("https://h/x/y", "no-such-agent", "/n")); !errors.Is(err, store.ErrAgentNotFound) {
		t.Errorf("unknown agent: got %v", err)
	}

	tk := task.New("https://h/x/y", "p")
	s.Create(ctx, tk, "fake")
	if err := s.UseSnapshot(ctx, tk.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Get(ctx, tk.ID); rec.SnapshotID == nil || *rec.SnapshotID != second.ID {
		t.Errorf("task snapshot = %v, want %d", rec.SnapshotID, second.ID)
	}
	if snaps, _ := s.ListSnapshots(ctx); len(snaps) != 3 || snaps[2].ID != second.ID {
		t.Errorf("listed %+v, want 3 with the just-used one last", snaps)
	}

	for range 2 {
		if err := s.MarkSnapshotDeleted(ctx, first.ID); err != nil {
			t.Fatal(err)
		}
	}
	if snaps, _ := s.ListSnapshots(ctx); len(snaps) != 2 {
		t.Errorf("listed %d after delete, want 2", len(snaps))
	}
	if err := s.UseSnapshot(ctx, tk.ID, first.ID); !errors.Is(err, store.ErrSnapshotNotFound) {
		t.Errorf("deleted snapshot: got %v", err)
	}
	if err := s.UseSnapshot(ctx, "nope", second.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown task: got %v", err)
	}
}
