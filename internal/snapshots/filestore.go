package snapshots

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/depak7/forgerun/internal/store"
)

// FileStore keeps a worker machine's record of its snapshots in a JSON
// file. Snapshots live on the machine that made them, so a worker that
// runs apart from the orchestrator keeps its own record. Same rules as
// the database: one current snapshot per repo and agent; replaced ones are
// retired, then deleted.
type FileStore struct {
	path string

	mu    sync.Mutex
	snaps map[int64]*store.Snapshot
	last  int64
}

var _ Store = (*FileStore)(nil)

type fileState struct {
	Last      int64             `json:"last_id"`
	Snapshots []*store.Snapshot `json:"snapshots"`
}

// OpenFileStore reads (or starts) the record at path.
func OpenFileStore(path string) (*FileStore, error) {
	fs := &FileStore{path: path, snaps: map[int64]*store.Snapshot{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fs, nil
	}
	if err != nil {
		return nil, err
	}
	var st fileState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	fs.last = st.Last
	for _, s := range st.Snapshots {
		fs.snaps[s.ID] = s
	}
	return fs, nil
}

// save writes the record atomically (a temporary file, then a rename), so
// a crash never leaves it half written. The caller holds fs.mu.
func (fs *FileStore) save() error {
	st := fileState{Last: fs.last}
	for _, s := range fs.snaps {
		st.Snapshots = append(st.Snapshots, s)
	}
	slices.SortFunc(st.Snapshots, func(a, b *store.Snapshot) int { return cmp.Compare(a.ID, b.ID) })
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fs.path), 0o755); err != nil {
		return err
	}
	tmp := fs.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, fs.path)
}

func (fs *FileStore) current(repo, agentID string) *store.Snapshot {
	for _, s := range fs.snaps {
		if s.Repo == repo && s.AgentID == agentID && s.State == store.SnapshotCurrent {
			return s
		}
	}
	return nil
}

func (fs *FileStore) CurrentSnapshot(_ context.Context, repo, agentID string) (store.Snapshot, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if s := fs.current(store.NormalizeRepo(repo), agentID); s != nil {
		return *s, nil
	}
	return store.Snapshot{}, fmt.Errorf("%w: %s for %s", store.ErrSnapshotNotFound, repo, agentID)
}

// UseSnapshot records that the snapshot was just used. Which task used it
// is the orchestrator's to know; it isn't kept here.
func (fs *FileStore) UseSnapshot(_ context.Context, _ string, id int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	s, ok := fs.snaps[id]
	if !ok || s.State == store.SnapshotDeleted {
		return fmt.Errorf("%w: %d", store.ErrSnapshotNotFound, id)
	}
	s.LastUsedAt = time.Now()
	return fs.save()
}

func (fs *FileStore) ReplaceSnapshot(_ context.Context, snap store.Snapshot) (store.Snapshot, *store.Snapshot, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	snap.Repo = store.NormalizeRepo(snap.Repo)
	var retired *store.Snapshot
	if old := fs.current(snap.Repo, snap.AgentID); old != nil {
		old.State = store.SnapshotRetired
		cp := *old
		retired = &cp
	}
	fs.last++
	now := time.Now()
	snap.ID, snap.State, snap.CreatedAt, snap.LastUsedAt, snap.DeletedAt = fs.last, store.SnapshotCurrent, now, now, nil
	fs.snaps[snap.ID] = &snap
	return snap, retired, fs.save()
}

func (fs *FileStore) MarkSnapshotDeleted(_ context.Context, id int64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	s, ok := fs.snaps[id]
	if !ok {
		return fmt.Errorf("%w: %d", store.ErrSnapshotNotFound, id)
	}
	if s.DeletedAt == nil {
		now := time.Now()
		s.DeletedAt = &now
	}
	s.State = store.SnapshotDeleted
	return fs.save()
}

func (fs *FileStore) ListSnapshots(context.Context) ([]store.Snapshot, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var out []store.Snapshot
	for _, s := range fs.snaps {
		if s.State != store.SnapshotDeleted {
			out = append(out, *s)
		}
	}
	slices.SortFunc(out, func(a, b store.Snapshot) int { // least recently used first
		return cmp.Or(a.LastUsedAt.Compare(b.LastUsedAt), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}
