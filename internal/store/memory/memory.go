// Package memory is an in-memory store.Store for tests and local experiments.
// Data is lost when the process exits.
package memory

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// modelKey mirrors the models table's primary key (agent, id).
type modelKey struct{ agent, id string }

// Store is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	tasks     map[string]*store.Record
	diffs     map[string]string // by task id, kept apart like the diff column
	models    map[modelKey]models.Variant
	snapshots map[int64]*store.Snapshot
	lastID    int64 // the last snapshot id handed out
}

var _ store.Store = (*Store)(nil)

// New returns an empty store that knows the models vs (no built-in models).
// Agents are implied by their models: an agent exists if any of vs belongs
// to it, and its default is the variant marked AgentDefault.
func New(vs ...models.Variant) *Store {
	s := &Store{tasks: map[string]*store.Record{}, diffs: map[string]string{}, models: map[modelKey]models.Variant{},
		snapshots: map[int64]*store.Snapshot{}}
	for _, v := range vs {
		s.models[modelKey{v.AgentID, v.ID}] = v
	}
	return s
}

func (s *Store) Close() {}

func (s *Store) Ping(context.Context) error { return nil }

func (s *Store) Create(_ context.Context, t task.Task, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[t.ID]; ok {
		return fmt.Errorf("creating task %s: already exists", t.ID)
	}
	m, ok := s.model(agentID, t.ModelID)
	if !ok { // mirrors the Postgres foreign keys / missing agent default
		return fmt.Errorf("creating task %s: no model %q for agent %s", t.ID, t.ModelID, agentID)
	}
	rec := &store.Record{ID: t.ID, Repo: t.Repo, Prompt: t.Prompt, AgentID: agentID,
		Model: m, State: task.Queued, PR: store.PullRequest{Requested: t.PR}, ThreadID: t.ID, CreatedAt: time.Now()}
	if t.ParentID != "" { // mirrors the Postgres thread rules and indexes
		parent, ok := s.tasks[t.ParentID]
		if !ok {
			return fmt.Errorf("creating task %s: parent %w: %s", t.ID, store.ErrNotFound, t.ParentID)
		}
		for _, other := range s.tasks {
			if other.ParentID == t.ParentID {
				return fmt.Errorf("creating task %s: %w", t.ID, store.ErrNotLatest)
			}
			if other.ThreadID == parent.ThreadID && (other.State == task.Queued || other.State == task.Running) {
				return fmt.Errorf("creating task %s: %w", t.ID, store.ErrThreadBusy)
			}
		}
		rec.ParentID, rec.ThreadID = parent.ID, parent.ThreadID
		rec.PR = parent.PR
		rec.PR.Requested, rec.PR.Error = t.PR, ""
	}
	s.tasks[t.ID] = rec
	return nil
}

func (s *Store) ListThreads(_ context.Context, limit int) ([]store.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byThread := map[string][]store.Record{}
	for _, r := range s.tasks {
		byThread[r.ThreadID] = append(byThread[r.ThreadID], *r)
	}
	var threads []store.Thread
	for id, recs := range byThread {
		slices.SortFunc(recs, func(a, b store.Record) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
		})
		root, latest := recs[0], recs[len(recs)-1]
		th := store.Thread{ID: id, Title: root.Prompt, Repo: root.Repo, Steps: len(recs), Latest: latest}
		for _, r := range recs {
			th.Agents = append(th.Agents, r.AgentID)
			if t := updated(r); t.After(th.UpdatedAt) {
				th.UpdatedAt = t
			}
		}
		threads = append(threads, th)
	}
	slices.SortFunc(threads, func(a, b store.Thread) int { // most recent activity first
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), strings.Compare(b.ID, a.ID))
	})
	if len(threads) > limit {
		threads = threads[:limit]
	}
	return threads, nil
}

// updated is when a task last changed.
func updated(r store.Record) time.Time {
	switch {
	case r.FinishedAt != nil:
		return *r.FinishedAt
	case r.StartedAt != nil:
		return *r.StartedAt
	}
	return r.CreatedAt
}

func (s *Store) CountTasks(context.Context) (map[task.State]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[task.State]int{}
	for _, r := range s.tasks {
		counts[r.State]++
	}
	return counts, nil
}

func (s *Store) ThreadTasks(_ context.Context, threadID string) ([]store.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var recs []store.Record
	for _, r := range s.tasks {
		if r.ThreadID == threadID {
			recs = append(recs, *r)
		}
	}
	slices.SortFunc(recs, func(a, b store.Record) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	return recs, nil
}

// model finds agent's model id, or the agent's default when id is "".
// The caller holds s.mu.
func (s *Store) model(agent, id string) (models.Variant, bool) {
	if id != "" {
		v, ok := s.models[modelKey{agent, id}]
		return v, ok
	}
	for _, v := range s.models {
		if v.AgentID == agent && v.AgentDefault {
			return v, true
		}
	}
	return models.Variant{}, false
}

func (s *Store) GetAgent(_ context.Context, id string) (store.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, found := store.Agent{ID: id, Name: id, Enabled: true}, false
	for _, v := range s.models {
		if v.AgentID != id {
			continue
		}
		found = true
		if v.AgentDefault {
			a.DefaultModelID = v.ID
		}
	}
	if !found {
		return store.Agent{}, fmt.Errorf("%w: %s", store.ErrAgentNotFound, id)
	}
	return a, nil
}

func (s *Store) ListAgents(ctx context.Context) ([]store.Agent, error) {
	s.mu.Lock()
	ids := map[string]bool{}
	for k := range s.models {
		ids[k.agent] = true
	}
	s.mu.Unlock() // GetAgent locks again
	var agents []store.Agent
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		a, err := s.GetAgent(ctx, id)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, nil
}

func (s *Store) ListModels(context.Context) ([]models.Variant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := slices.Collect(maps.Values(s.models))
	slices.SortFunc(vs, func(a, b models.Variant) int {
		return cmp.Or(strings.Compare(a.AgentID, b.AgentID), strings.Compare(a.ID, b.ID))
	})
	return vs, nil
}

func (s *Store) Start(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.transition(id, task.Queued, task.Running)
	if err != nil {
		return err
	}
	now := time.Now()
	r.StartedAt = &now
	return nil
}

func (s *Store) Finish(_ context.Context, id string, o store.Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.transition(id, task.Running, o.State)
	if err != nil {
		return err
	}
	r.Error, r.LogPath, r.HasDiff = o.Error, o.LogPath, o.Diff != ""
	s.diffs[id] = o.Diff
	r.BaseCommit, r.Summary, r.PR.Error = o.BaseCommit, o.Summary, o.PRError
	// A follow-up inherited its thread's pull request; keep it if this run didn't publish.
	r.PR.URL, r.PR.Number, r.PR.Branch = cmp.Or(o.PRURL, r.PR.URL), cmp.Or(o.PRNumber, r.PR.Number), cmp.Or(o.PRBranch, r.PR.Branch)
	now := time.Now()
	r.FinishedAt = &now
	return nil
}

func (s *Store) Claim(_ context.Context) (store.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var oldest *store.Record
	for _, r := range s.tasks {
		if r.State == task.Queued && (oldest == nil || r.CreatedAt.Before(oldest.CreatedAt)) {
			oldest = r
		}
	}
	if oldest == nil {
		return store.Record{}, false, nil
	}
	now := time.Now()
	oldest.State, oldest.StartedAt = task.Running, &now
	return *oldest, true, nil
}

func (s *Store) Cancel(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.transition(id, task.Queued, task.Cancelled)
	if err != nil {
		return err
	}
	now := time.Now()
	r.Error, r.FinishedAt = "cancelled by user", &now
	return nil
}

func (s *Store) FailRunning(_ context.Context, reason string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	now := time.Now()
	for _, r := range s.tasks {
		if r.State == task.Running {
			r.State, r.Error, r.FinishedAt = task.Failed, reason, &now
			n++
		}
	}
	return n, nil
}

func (s *Store) Get(_ context.Context, id string) (store.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.tasks[id]
	if !ok {
		return store.Record{}, fmt.Errorf("%w: %s", store.ErrNotFound, id)
	}
	return *r, nil
}

func (s *Store) Diff(_ context.Context, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return "", fmt.Errorf("%w: %s", store.ErrNotFound, id)
	}
	return s.diffs[id], nil
}

func (s *Store) List(_ context.Context, limit int) ([]store.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs := make([]store.Record, 0, len(s.tasks))
	for _, r := range s.tasks {
		recs = append(recs, *r)
	}
	slices.SortFunc(recs, func(a, b store.Record) int { return b.CreatedAt.Compare(a.CreatedAt) }) // newest first
	if len(recs) > limit {
		recs = recs[:limit]
	}
	return recs, nil
}

func (s *Store) CurrentSnapshot(_ context.Context, repo, agentID string) (store.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap := s.current(store.NormalizeRepo(repo), agentID); snap != nil {
		return *snap, nil
	}
	return store.Snapshot{}, fmt.Errorf("%w: %s for %s", store.ErrSnapshotNotFound, repo, agentID)
}

// current finds the current snapshot for a normalized repo, or nil.
// The caller holds s.mu.
func (s *Store) current(repo, agentID string) *store.Snapshot {
	for _, snap := range s.snapshots {
		if snap.Repo == repo && snap.AgentID == agentID && snap.State == store.SnapshotCurrent {
			return snap
		}
	}
	return nil
}

func (s *Store) UseSnapshot(_ context.Context, taskID string, snapshotID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.snapshots[snapshotID]
	if !ok || snap.State == store.SnapshotDeleted {
		return fmt.Errorf("%w: %d", store.ErrSnapshotNotFound, snapshotID)
	}
	r, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("%w: %s", store.ErrNotFound, taskID)
	}
	snap.LastUsedAt = time.Now()
	r.SnapshotID = &snapshotID
	return nil
}

func (s *Store) ReplaceSnapshot(_ context.Context, snap store.Snapshot) (store.Snapshot, *store.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasAgent(snap.AgentID) { // mirrors the Postgres foreign key
		return store.Snapshot{}, nil, fmt.Errorf("saving snapshot: %w: %s", store.ErrAgentNotFound, snap.AgentID)
	}
	snap.Repo = store.NormalizeRepo(snap.Repo)
	var retired *store.Snapshot
	if old := s.current(snap.Repo, snap.AgentID); old != nil {
		old.State = store.SnapshotRetired
		cp := *old // a copy, so the caller can't change the stored one
		retired = &cp
	}
	s.lastID++
	now := time.Now()
	snap.ID, snap.State, snap.CreatedAt, snap.LastUsedAt, snap.DeletedAt = s.lastID, store.SnapshotCurrent, now, now, nil
	s.snapshots[snap.ID] = &snap
	return snap, retired, nil
}

// hasAgent reports whether any model belongs to agent. The caller holds s.mu.
func (s *Store) hasAgent(agent string) bool {
	for k := range s.models {
		if k.agent == agent {
			return true
		}
	}
	return false
}

func (s *Store) MarkSnapshotDeleted(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.snapshots[id]
	if !ok {
		return fmt.Errorf("%w: %d", store.ErrSnapshotNotFound, id)
	}
	if snap.DeletedAt == nil {
		now := time.Now()
		snap.DeletedAt = &now
	}
	snap.State = store.SnapshotDeleted
	return nil
}

func (s *Store) ListSnapshots(context.Context) ([]store.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var snaps []store.Snapshot
	for _, snap := range s.snapshots {
		if snap.State != store.SnapshotDeleted {
			snaps = append(snaps, *snap)
		}
	}
	slices.SortFunc(snaps, func(a, b store.Snapshot) int { // least recently used first
		return cmp.Or(a.LastUsedAt.Compare(b.LastUsedAt), cmp.Compare(a.ID, b.ID))
	})
	return snaps, nil
}

// transition checks and applies a state change. The caller holds s.mu.
func (s *Store) transition(id string, from, to task.State) (*store.Record, error) {
	if err := from.CheckTransition(to); err != nil {
		return nil, err
	}
	r, ok := s.tasks[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", store.ErrNotFound, id)
	}
	if r.State != from {
		return nil, fmt.Errorf("%w: task %s is %s, not %s", task.ErrInvalidTransition, id, r.State, from)
	}
	r.State = to
	return r, nil
}
