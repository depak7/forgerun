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

// Store is safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	tasks  map[string]*store.Record
	models map[string]models.Variant
}

var _ store.Store = (*Store)(nil)

var autoModel = models.Variant{ID: "auto", Agent: "cursor", Family: "auto", Name: "Auto", IsDefault: true, ZDR: true}

// New returns an empty store that knows auto plus vs.
func New(vs ...models.Variant) *Store {
	s := &Store{tasks: map[string]*store.Record{}, models: map[string]models.Variant{"auto": autoModel}}
	for _, v := range vs {
		s.models[v.ID] = v
	}
	return s
}

func (s *Store) Close() {}

func (s *Store) Ping(context.Context) error { return nil }

func (s *Store) Create(_ context.Context, t task.Task, agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[t.ID]; ok {
		return fmt.Errorf("creating task %s: already exists", t.ID)
	}
	m, ok := s.models[cmp.Or(t.ModelID, "auto")]
	if !ok { // mirrors the Postgres foreign key
		return fmt.Errorf("creating task %s: unknown model %q", t.ID, t.ModelID)
	}
	s.tasks[t.ID] = &store.Record{ID: t.ID, Repo: t.Repo, Prompt: t.Prompt, Agent: agent,
		Model: m, State: task.Queued, CreatedAt: time.Now()}
	return nil
}

func (s *Store) ListModels(context.Context) ([]models.Variant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := slices.Collect(maps.Values(s.models))
	slices.SortFunc(vs, func(a, b models.Variant) int { return strings.Compare(a.ID, b.ID) })
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
	r.Error, r.LogPath, r.PatchPath = o.Error, o.LogPath, o.PatchPath
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
