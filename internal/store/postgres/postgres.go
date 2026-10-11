// Package postgres implements store.Store on PostgreSQL using pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// Store is a store.Store backed by a Postgres connection pool.
// It is safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
}

var _ store.Store = (*Store)(nil)

// Open connects to Postgres and applies pending migrations.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil { // pgxpool.New is lazy; Ping proves the DB is reachable
		pool.Close()
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Create(ctx context.Context, t task.Task, agent string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tasks (id, repo, prompt, agent, state) VALUES ($1, $2, $3, $4, $5)`,
		t.ID, t.Repo, t.Prompt, agent, string(task.Queued))
	if err != nil {
		return fmt.Errorf("creating task %s: %w", t.ID, err)
	}
	return nil
}

func (s *Store) Start(ctx context.Context, id string) error {
	return s.transition(ctx, id, task.Queued, task.Running,
		`UPDATE tasks SET state = $3, started_at = now() WHERE id = $1 AND state = $2`)
}

func (s *Store) Finish(ctx context.Context, id string, o store.Outcome) error {
	return s.transition(ctx, id, task.Running, o.State,
		`UPDATE tasks SET state = $3, error = $4, log_path = $5, patch_path = $6, finished_at = now()
		 WHERE id = $1 AND state = $2`,
		nullable(o.Error), nullable(o.LogPath), nullable(o.PatchPath))
}

func (s *Store) Claim(ctx context.Context) (store.Record, bool, error) {
	// SKIP LOCKED: rows another worker is claiming right now are skipped
	// instead of waited on, so concurrent workers each get a different task.
	row := s.pool.QueryRow(ctx, `
		UPDATE tasks SET state = 'running', started_at = now()
		WHERE state = 'queued' AND id = (
			SELECT id FROM tasks WHERE state = 'queued'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1)
		RETURNING `+selectColumns)
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Record{}, false, nil
	}
	if err != nil {
		return store.Record{}, false, fmt.Errorf("claiming task: %w", err)
	}
	return rec, true, nil
}

func (s *Store) Cancel(ctx context.Context, id string) error {
	return s.transition(ctx, id, task.Queued, task.Cancelled,
		`UPDATE tasks SET state = $3, error = $4, finished_at = now() WHERE id = $1 AND state = $2`,
		"cancelled by user")
}

func (s *Store) FailRunning(ctx context.Context, reason string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tasks SET state = 'failed', error = $1, finished_at = now() WHERE state = 'running'`, reason)
	if err != nil {
		return 0, fmt.Errorf("failing running tasks: %w", err)
	}
	return tag.RowsAffected(), nil
}

// transition runs an UPDATE guarded by "AND state = from" (compare-and-swap),
// so two processes can never both move the same task.
// SQL args: $1 = id, $2 = from, $3 = to, then extra.
func (s *Store) transition(ctx context.Context, id string, from, to task.State, sql string, extra ...any) error {
	if err := from.CheckTransition(to); err != nil {
		return err
	}
	args := append([]any{id, string(from), string(to)}, extra...)
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("updating task %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 { // not found, or state wasn't `from`
		rec, err := s.Get(ctx, id)
		if err != nil {
			return err // includes store.ErrNotFound
		}
		return fmt.Errorf("%w: task %s is %s, not %s", task.ErrInvalidTransition, id, rec.State, from)
	}
	return nil
}

const selectColumns = `id, repo, prompt, agent, state, error, log_path, patch_path, created_at, started_at, finished_at`

func (s *Store) Get(ctx context.Context, id string) (store.Record, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+selectColumns+` FROM tasks WHERE id = $1`, id)
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Record{}, fmt.Errorf("%w: %s", store.ErrNotFound, id)
	}
	return rec, err
}

func (s *Store) List(ctx context.Context, limit int) ([]store.Record, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+selectColumns+` FROM tasks ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	defer rows.Close()

	var recs []store.Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	if err := rows.Err(); err != nil { // a failure mid-iteration only shows up here
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	return recs, nil
}

// scanRecord reads one row in selectColumns order. pgx.Row is satisfied by
// both QueryRow's result and pgx.Rows, so Get and List share this.
func scanRecord(row pgx.Row) (store.Record, error) {
	var (
		rec                        store.Record
		state                      string
		errMsg, logPath, patchPath *string // nil = NULL
		startedAt, finishedAt      *time.Time
	)
	err := row.Scan(&rec.ID, &rec.Repo, &rec.Prompt, &rec.Agent, &state,
		&errMsg, &logPath, &patchPath, &rec.CreatedAt, &startedAt, &finishedAt)
	if err != nil {
		return store.Record{}, err
	}
	rec.State = task.State(state)
	if !rec.State.Valid() {
		return store.Record{}, fmt.Errorf("task %s has unknown state %q", rec.ID, state)
	}
	rec.Error, rec.LogPath, rec.PatchPath = deref(errMsg), deref(logPath), deref(patchPath)
	rec.StartedAt, rec.FinishedAt = startedAt, finishedAt
	return rec, nil
}

// nullable maps "" to SQL NULL.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// deref maps SQL NULL to "".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
