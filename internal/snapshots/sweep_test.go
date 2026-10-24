package snapshots

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
)

func TestPlan(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	snap := func(id int64, state store.SnapshotState, usedAgo time.Duration, mb int64) store.Snapshot {
		return store.Snapshot{ID: id, State: state, LastUsedAt: now.Add(-usedAgo), SizeBytes: mb * 1e6}
	}
	day := 24 * time.Hour
	// Least recently used first, as ListSnapshots returns them.
	snaps := []store.Snapshot{
		snap(1, store.SnapshotCurrent, 10*day, 100),
		snap(2, store.SnapshotRetired, 3*day, 50),
		snap(3, store.SnapshotCurrent, 2*day, 300),
		snap(4, store.SnapshotCurrent, time.Hour, 200),
	}
	tests := []struct {
		name      string
		limits    Limits
		want      map[int64]Reason
		keptBytes int64
	}{
		{"no limits: only retired go", Limits{}, map[int64]Reason{2: ReasonRetired}, 600e6},
		{"max age", Limits{MaxAge: 7 * day}, map[int64]Reason{1: ReasonExpired, 2: ReasonRetired}, 500e6},
		{"budget evicts least recently used", Limits{Budget: 450e6},
			map[int64]Reason{1: ReasonOverBudget, 2: ReasonRetired, 3: ReasonOverBudget}, 200e6},
		{"both", Limits{MaxAge: 7 * day, Budget: 450e6},
			map[int64]Reason{1: ReasonExpired, 2: ReasonRetired, 3: ReasonOverBudget}, 200e6},
		{"within budget", Limits{Budget: 600e6}, map[int64]Reason{2: ReasonRetired}, 600e6},
		{"tiny budget empties everything", Limits{Budget: 1},
			map[int64]Reason{1: ReasonOverBudget, 2: ReasonRetired, 3: ReasonOverBudget, 4: ReasonOverBudget}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evict, kept, keptBytes := plan(snaps, now, tc.limits)
			got := map[int64]Reason{}
			for _, e := range evict {
				got[e.Snapshot.ID] = e.Reason
			}
			if len(got) != len(tc.want) {
				t.Errorf("evicted %v, want %v", got, tc.want)
			}
			for id, r := range tc.want {
				if got[id] != r {
					t.Errorf("snapshot %d: got %q, want %q", id, got[id], r)
				}
			}
			if keptBytes != tc.keptBytes || kept != len(snaps)-len(tc.want) {
				t.Errorf("kept %d snapshots / %d bytes, want %d / %d", kept, keptBytes, len(snaps)-len(tc.want), tc.keptBytes)
			}
		})
	}
}

// TestSweep runs every sweep stage against real snapshots.
func TestSweep(t *testing.T) {
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot real VMs")
	}
	ctx := context.Background()
	s := memory.New(models.Variant{ID: "auto", AgentID: "fake", Family: "auto", AgentDefault: true})
	m := &Manager{Store: s, Prefix: testPrefix}

	vm, err := msb.New(ctx, "sweep-"+time.Now().Format("150405"), sandbox.Config{
		Image: "depak7/forgerun-agent-cursor:dev", CPUs: 1, MemoryMiB: 512, MaxLifetime: 3 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Destroy()
	var refs []string
	t.Cleanup(func() {
		for _, ref := range refs {
			msb.DeleteSnapshot(context.Background(), ref)
		}
	})
	// take snapshots vm into repo's group; record saves it in the store.
	take := func(repo string, record bool) store.Snapshot {
		t.Helper()
		img, err := vm.TakeSnapshot(ctx, m.Group(repo, "fake"), task.New(repo, "").ID)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, img.Ref)
		if !record {
			return store.Snapshot{Ref: img.Ref}
		}
		saved, _, err := s.ReplaceSnapshot(ctx, store.Snapshot{Repo: repo, AgentID: "fake", Ref: img.Ref, Commit: "c", SizeBytes: img.SizeBytes})
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}

	old := take("https://example.com/a", true)
	a := take("https://example.com/a", true) // retires old, as if its delete failed
	b := take("https://example.com/b", true)
	orphan := take("https://example.com/c", false)
	// In the store, but its files are gone.
	if _, _, err := s.ReplaceSnapshot(ctx, store.Snapshot{Repo: "https://example.com/d", AgentID: "fake", Ref: "/nonexistent/snap", Commit: "c"}); err != nil {
		t.Fatal(err)
	}

	// 1. Now: the retired one is finished off, the missing one forgotten;
	//    the orphan is too new to touch.
	rep, err := m.Sweep(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sweep 1: %s", rep)
	if rep.Deleted[ReasonRetired] != 1 || rep.Missing != 1 || rep.Orphans != 0 || rep.Kept != 2 {
		t.Errorf("sweep 1: %s", rep)
	}
	assertGone(t, old.Ref, true)
	assertGone(t, orphan.Ref, false)
	assertListed(t, s, a.ID, b.ID) // old is deleted, missing is forgotten

	// 2. Later: the orphan is old enough to delete.
	rep, err = m.Sweep(ctx, time.Now().Add(orphanAge+time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sweep 2: %s", rep)
	if rep.Orphans != 1 {
		t.Errorf("sweep 2: %s", rep)
	}
	assertGone(t, orphan.Ref, true)

	// 3. A budget only one fits in: the least recently used (a) goes.
	m.Limits = Limits{Budget: b.SizeBytes}
	if rep, err = m.Sweep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Logf("sweep 3: %s", rep)
	if rep.Deleted[ReasonOverBudget] != 1 {
		t.Errorf("sweep 3: %s", rep)
	}
	assertGone(t, a.Ref, true)
	assertListed(t, s, b.ID)

	// 4. A week later with a 7-day max age, b expires too.
	m.Limits = Limits{MaxAge: 7 * 24 * time.Hour}
	if rep, err = m.Sweep(ctx, time.Now().Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Logf("sweep 4: %s", rep)
	if rep.Deleted[ReasonExpired] != 1 || rep.Kept != 0 {
		t.Errorf("sweep 4: %s", rep)
	}
	assertGone(t, b.Ref, true)
	assertListed(t, s)
}

func assertGone(t *testing.T, ref string, want bool) {
	t.Helper()
	if got := msb.SnapshotGone(ref); got != want {
		t.Errorf("%s: gone = %v, want %v", ref, got, want)
	}
}

func assertListed(t *testing.T, s store.Store, ids ...int64) {
	t.Helper()
	snaps, _ := s.ListSnapshots(context.Background())
	var got []int64
	for _, snap := range snaps {
		got = append(got, snap.ID)
	}
	slices.Sort(got)
	slices.Sort(ids)
	if !slices.Equal(got, ids) {
		t.Errorf("listed snapshots %v, want %v", got, ids)
	}
}
