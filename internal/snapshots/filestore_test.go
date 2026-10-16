package snapshots

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/depak7/forgerun/internal/store"
)

func TestFileStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshots.json")
	fs, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.CurrentSnapshot(ctx, "https://github.com/o/r", "cursor"); !errors.Is(err, store.ErrSnapshotNotFound) {
		t.Errorf("empty: %v", err)
	}
	a, retired, err := fs.ReplaceSnapshot(ctx, store.Snapshot{Repo: "https://github.com/o/r.git", AgentID: "cursor", Ref: "/a", SizeBytes: 5})
	if err != nil || retired != nil || a.ID != 1 || a.Repo != "https://github.com/o/r" {
		t.Fatalf("first: %+v, %v, %v", a, retired, err)
	}
	b, retired, _ := fs.ReplaceSnapshot(ctx, store.Snapshot{Repo: "https://github.com/o/r", AgentID: "cursor", Ref: "/b"})
	if retired == nil || retired.ID != a.ID || b.ID != 2 {
		t.Errorf("replacing: %+v retired %+v", b, retired)
	}
	fs.MarkSnapshotDeleted(ctx, a.ID)

	// It survives a restart.
	again, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := again.CurrentSnapshot(ctx, "https://github.com/o/r", "cursor")
	if err != nil || cur.ID != b.ID || cur.Ref != "/b" {
		t.Errorf("after reopening: %+v, %v", cur, err)
	}
	if list, _ := again.ListSnapshots(ctx); len(list) != 1 {
		t.Errorf("listed %d, want 1 (the deleted one is gone)", len(list))
	}
	if err := again.UseSnapshot(ctx, "t-1", b.ID); err != nil {
		t.Error(err)
	}
	if c, _, _ := again.ReplaceSnapshot(ctx, store.Snapshot{Repo: "https://github.com/o/r", AgentID: "cursor"}); c.ID != 3 {
		t.Errorf("IDs must keep counting after a restart: %d", c.ID)
	}
}
