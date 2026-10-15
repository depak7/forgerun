// Package postgres implements store.Store on PostgreSQL using pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/depak7/forgerun/internal/models"
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

func (s *Store) Create(ctx context.Context, t task.Task, agentID string) error {
	// An empty model id means the agent's default_model_id. If the agent has
	// none (or doesn't exist), model_id is NULL and the insert fails.
	// A follow-up (parent set) joins the parent's thread and inherits its
	// pull request; if the parent doesn't exist, nothing is inserted.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO tasks (id, repo, prompt, agent_id, state, model_id, pr_requested,
		                   parent_id, thread_id, pr_url, pr_number, pr_branch)
		SELECT $1, $2, $3, $4, $5,
		       COALESCE(NULLIF($6, ''), (SELECT default_model_id FROM agents WHERE id = $4)),
		       $7 OR p.id IS NOT NULL,
		       p.id, COALESCE(p.thread_id, $1), p.pr_url, p.pr_number, p.pr_branch
		FROM (SELECT NULLIF($8::text, '') AS parent) want
		LEFT JOIN tasks p ON p.id = want.parent
		WHERE want.parent IS NULL OR p.id IS NOT NULL`,
		t.ID, t.Repo, t.Prompt, agentID, string(task.Queued), t.ModelID, t.PR, t.ParentID)
	if err != nil {
		return fmt.Errorf("creating task %s: %w", t.ID, threadError(err)) // includes the FK error for an unknown model
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("creating task %s: parent %w: %s", t.ID, store.ErrNotFound, t.ParentID)
	}
	return nil
}

func (s *Store) ListThreads(ctx context.Context, limit int) ([]store.Thread, error) {
	// One row per thread: its first task, size, agents in order, newest step.
	rows, err := s.pool.Query(ctx, `
		SELECT root.id, root.prompt, root.repo, count(*),
		       array_agg(t.agent_id ORDER BY t.created_at, t.id),
		       (array_agg(t.id ORDER BY t.created_at DESC, t.id DESC))[1],
		       max(COALESCE(t.finished_at, t.started_at, t.created_at)) AS updated
		FROM tasks t JOIN tasks root ON root.id = t.thread_id
		GROUP BY root.id, root.prompt, root.repo
		ORDER BY updated DESC, root.id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing threads: %w", err)
	}
	var threads []store.Thread
	var latestIDs []string
	for rows.Next() {
		var th store.Thread
		var latest string
		if err := rows.Scan(&th.ID, &th.Title, &th.Repo, &th.Steps, &th.Agents, &latest, &th.UpdatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("listing threads: %w", err)
		}
		threads = append(threads, th)
		latestIDs = append(latestIDs, latest)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing threads: %w", err)
	}

	// Then each thread's newest step, in one query.
	recs, err := s.pool.Query(ctx, `SELECT `+selectColumns+fromTasks+` WHERE t.id = ANY($1)`, latestIDs)
	if err != nil {
		return nil, fmt.Errorf("listing threads: %w", err)
	}
	latest, err := pgx.CollectRows(recs, func(row pgx.CollectableRow) (store.Record, error) { return scanRecord(row) })
	if err != nil {
		return nil, fmt.Errorf("listing threads: %w", err)
	}
	byID := make(map[string]store.Record, len(latest))
	for _, r := range latest {
		byID[r.ID] = r
	}
	for i := range threads {
		threads[i].Latest = byID[latestIDs[i]]
	}
	return threads, nil
}

func (s *Store) CountTasks(ctx context.Context) (map[task.State]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT state, count(*) FROM tasks GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("counting tasks: %w", err)
	}
	counts := map[task.State]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("counting tasks: %w", err)
		}
		counts[task.State(state)] = n
	}
	rows.Close()
	return counts, rows.Err()
}

// threadError turns a violation of a thread's unique indexes into the
// store's error for it. Postgres reports it as code 23505 and names the index.
func threadError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "tasks_one_followup_idx":
			return store.ErrNotLatest
		case "tasks_one_active_per_thread_idx":
			return store.ErrThreadBusy
		}
	}
	return err
}

func (s *Store) ThreadTasks(ctx context.Context, threadID string) ([]store.Record, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+selectColumns+fromTasks+` WHERE t.thread_id = $1 ORDER BY t.created_at, t.id`, threadID)
	if err != nil {
		return nil, fmt.Errorf("listing thread %s: %w", threadID, err)
	}
	recs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Record, error) { return scanRecord(row) })
	if err != nil {
		return nil, fmt.Errorf("listing thread %s: %w", threadID, err)
	}
	return recs, nil
}

func (s *Store) Start(ctx context.Context, id string) error {
	return s.transition(ctx, id, task.Queued, task.Running,
		`UPDATE tasks SET state = $3, started_at = now() WHERE id = $1 AND state = $2`)
}

func (s *Store) Finish(ctx context.Context, id string, o store.Outcome) error {
	return s.transition(ctx, id, task.Running, o.State,
		`UPDATE tasks SET state = $3, error = $4, log_path = $5, patch_path = $6, finished_at = now(),
			base_commit = $7, summary = $8, pr_error = $10,
			-- a follow-up inherited its thread's pull request; keep it if this run didn't publish
			pr_url = COALESCE($9, pr_url), pr_number = COALESCE($11, pr_number), pr_branch = COALESCE($12, pr_branch)
		 WHERE id = $1 AND state = $2`,
		nullable(o.Error), nullable(o.LogPath), nullable(o.PatchPath),
		nullable(o.BaseCommit), nullable(o.Summary), nullable(o.PRURL), nullable(o.PRError),
		nullableInt(o.PRNumber), nullable(o.PRBranch))
}

func (s *Store) Claim(ctx context.Context) (store.Record, bool, error) {
	// SKIP LOCKED: rows another worker is claiming right now are skipped
	// instead of waited on. FROM models joins the model so RETURNING has it.
	row := s.pool.QueryRow(ctx, `
		UPDATE tasks AS t SET state = 'running', started_at = now()
		FROM models m, agents a
		WHERE m.agent_id = t.agent_id AND m.id = t.model_id AND a.id = t.agent_id
			AND t.state = 'queued' AND t.id = (
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

// modelColumns is a model row; agent_default is derived from its agent's
// default_model_id. Queries alias models as m and agents as a.
const modelColumns = `m.id, m.agent_id, m.family, m.name, m.effort, m.thinking, m.fast, m.is_default,
	COALESCE(m.id = a.default_model_id, false), m.zdr`

// selectColumns is the task (aliased t) plus its model.
const selectColumns = `t.id, t.repo, t.prompt, t.agent_id, t.state, t.error, t.log_path, t.patch_path,
	t.snapshot_id, t.pr_requested, t.pr_url, t.pr_error, t.base_commit, t.summary,
	t.parent_id, t.thread_id, t.pr_number, t.pr_branch, t.created_at, t.started_at, t.finished_at, ` + modelColumns

const fromTasks = ` FROM tasks t
	JOIN models m ON m.agent_id = t.agent_id AND m.id = t.model_id
	JOIN agents a ON a.id = t.agent_id`

func (s *Store) Get(ctx context.Context, id string) (store.Record, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+selectColumns+fromTasks+` WHERE t.id = $1`, id)
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Record{}, fmt.Errorf("%w: %s", store.ErrNotFound, id)
	}
	return rec, err
}

func (s *Store) List(ctx context.Context, limit int) ([]store.Record, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+selectColumns+fromTasks+` ORDER BY t.created_at DESC, t.id DESC LIMIT $1`, limit)
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

func (s *Store) GetAgent(ctx context.Context, id string) (store.Agent, error) {
	var a store.Agent
	var defaultModel *string
	err := s.pool.QueryRow(ctx, `SELECT id, name, enabled, default_model_id FROM agents WHERE id = $1`, id).
		Scan(&a.ID, &a.Name, &a.Enabled, &defaultModel)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Agent{}, fmt.Errorf("%w: %s", store.ErrAgentNotFound, id)
	}
	if err != nil {
		return store.Agent{}, fmt.Errorf("getting agent %s: %w", id, err)
	}
	a.DefaultModelID = deref(defaultModel)
	return a, nil
}

func (s *Store) ListAgents(ctx context.Context) ([]store.Agent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, enabled, default_model_id FROM agents WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}
	agents, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Agent, error) {
		var a store.Agent
		var defaultModel *string
		err := row.Scan(&a.ID, &a.Name, &a.Enabled, &defaultModel)
		a.DefaultModelID = deref(defaultModel)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}
	return agents, nil
}

func (s *Store) ListModels(ctx context.Context) ([]models.Variant, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+modelColumns+`
		FROM models m JOIN agents a ON a.id = m.agent_id
		WHERE m.enabled AND a.enabled
		ORDER BY m.agent_id, m.family, m.id`)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	vs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.Variant, error) {
		var v models.Variant
		err := row.Scan(&v.ID, &v.AgentID, &v.Family, &v.Name, &v.Effort, &v.Thinking, &v.Fast, &v.IsDefault, &v.AgentDefault, &v.ZDR)
		return v, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	return vs, nil
}

// snapshotColumns is a snapshot row, in scanSnapshot's order.
const snapshotColumns = `id, repo, agent_id, ref, commit_sha, deps_fingerprint, size_bytes, state,
	created_at, last_used_at, deleted_at`

func (s *Store) CurrentSnapshot(ctx context.Context, repo, agentID string) (store.Snapshot, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+snapshotColumns+` FROM snapshots
		WHERE repo = $1 AND agent_id = $2 AND state = 'current'`, store.NormalizeRepo(repo), agentID)
	snap, err := scanSnapshot(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Snapshot{}, fmt.Errorf("%w: %s for %s", store.ErrSnapshotNotFound, repo, agentID)
	}
	if err != nil {
		return store.Snapshot{}, fmt.Errorf("getting snapshot of %s for %s: %w", repo, agentID, err)
	}
	return snap, nil
}

func (s *Store) UseSnapshot(ctx context.Context, taskID string, snapshotID int64) error {
	// Both updates happen, or neither: BeginFunc commits if the function
	// returns nil and rolls back if it returns an error.
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE snapshots SET last_used_at = now()
			WHERE id = $1 AND state <> 'deleted'`, snapshotID)
		if err != nil {
			return fmt.Errorf("using snapshot %d: %w", snapshotID, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: %d", store.ErrSnapshotNotFound, snapshotID)
		}
		tag, err = tx.Exec(ctx, `UPDATE tasks SET snapshot_id = $2 WHERE id = $1`, taskID, snapshotID)
		if err != nil {
			return fmt.Errorf("recording snapshot of task %s: %w", taskID, err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: %s", store.ErrNotFound, taskID)
		}
		return nil
	})
}

func (s *Store) ReplaceSnapshot(ctx context.Context, snap store.Snapshot) (store.Snapshot, *store.Snapshot, error) {
	repo := store.NormalizeRepo(snap.Repo)
	var saved store.Snapshot
	var retired *store.Snapshot
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Two tasks for the same repo and agent may finish together. This
		// lock (released at commit) makes the second wait for the first, so
		// it then retires the first one's snapshot. Without it, both would
		// see the same "previous" snapshot and the second insert would
		// break the one-current-snapshot index.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || ' ' || $2, 0))`,
			repo, snap.AgentID); err != nil {
			return err
		}
		old, err := scanSnapshot(tx.QueryRow(ctx, `UPDATE snapshots SET state = 'retired'
			WHERE repo = $1 AND agent_id = $2 AND state = 'current'
			RETURNING `+snapshotColumns, repo, snap.AgentID))
		switch {
		case err == nil:
			retired = &old
		case !errors.Is(err, pgx.ErrNoRows): // no rows = first snapshot of this repo
			return err
		}
		saved, err = scanSnapshot(tx.QueryRow(ctx, `INSERT INTO snapshots
			(repo, agent_id, ref, commit_sha, deps_fingerprint, size_bytes)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+snapshotColumns,
			repo, snap.AgentID, snap.Ref, snap.Commit, snap.DepsFingerprint, snap.SizeBytes))
		return err // includes the FK error for an unknown agent
	})
	if err != nil {
		return store.Snapshot{}, nil, fmt.Errorf("saving snapshot of %s for %s: %w", repo, snap.AgentID, err)
	}
	return saved, retired, nil
}

func (s *Store) MarkSnapshotDeleted(ctx context.Context, id int64) error {
	// COALESCE keeps the first deletion time if it's marked twice.
	tag, err := s.pool.Exec(ctx, `UPDATE snapshots
		SET state = 'deleted', deleted_at = COALESCE(deleted_at, now()) WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("marking snapshot %d deleted: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %d", store.ErrSnapshotNotFound, id)
	}
	return nil
}

func (s *Store) ListSnapshots(ctx context.Context) ([]store.Snapshot, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+snapshotColumns+` FROM snapshots
		WHERE state <> 'deleted' ORDER BY last_used_at, id`)
	if err != nil {
		return nil, fmt.Errorf("listing snapshots: %w", err)
	}
	snaps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Snapshot, error) {
		return scanSnapshot(row)
	})
	if err != nil {
		return nil, fmt.Errorf("listing snapshots: %w", err)
	}
	return snaps, nil
}

func scanSnapshot(row pgx.Row) (store.Snapshot, error) {
	var snap store.Snapshot
	var state string
	err := row.Scan(&snap.ID, &snap.Repo, &snap.AgentID, &snap.Ref, &snap.Commit, &snap.DepsFingerprint,
		&snap.SizeBytes, &state, &snap.CreatedAt, &snap.LastUsedAt, &snap.DeletedAt)
	snap.State = store.SnapshotState(state)
	return snap, err
}

// scanRecord reads one row in selectColumns order. pgx.Row is satisfied by
// both QueryRow's result and pgx.Rows, so Get and List share this.
func scanRecord(row pgx.Row) (store.Record, error) {
	var (
		rec                                 store.Record
		state                               string
		errMsg, logPath, patchPath          *string // nil = NULL
		prURL, prError, baseCommit, summary *string
		parentID, prBranch                  *string
		prNumber                            *int
		startedAt, finishedAt               *time.Time
	)
	err := row.Scan(&rec.ID, &rec.Repo, &rec.Prompt, &rec.AgentID, &state,
		&errMsg, &logPath, &patchPath, &rec.SnapshotID,
		&rec.PR.Requested, &prURL, &prError, &baseCommit, &summary,
		&parentID, &rec.ThreadID, &prNumber, &prBranch, &rec.CreatedAt, &startedAt, &finishedAt,
		&rec.Model.ID, &rec.Model.AgentID, &rec.Model.Family, &rec.Model.Name, &rec.Model.Effort,
		&rec.Model.Thinking, &rec.Model.Fast, &rec.Model.IsDefault, &rec.Model.AgentDefault, &rec.Model.ZDR)
	if err != nil {
		return store.Record{}, err
	}
	rec.State = task.State(state)
	if !rec.State.Valid() {
		return store.Record{}, fmt.Errorf("task %s has unknown state %q", rec.ID, state)
	}
	rec.Error, rec.LogPath, rec.PatchPath = deref(errMsg), deref(logPath), deref(patchPath)
	rec.PR.URL, rec.PR.Error = deref(prURL), deref(prError)
	rec.BaseCommit, rec.Summary = deref(baseCommit), deref(summary)
	rec.ParentID, rec.PR.Branch = deref(parentID), deref(prBranch)
	if prNumber != nil {
		rec.PR.Number = *prNumber
	}
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

// nullableInt maps 0 to SQL NULL.
func nullableInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// deref maps SQL NULL to "".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
