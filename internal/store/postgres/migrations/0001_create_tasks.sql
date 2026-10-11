CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    repo        TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    agent       TEXT NOT NULL,
    state       TEXT NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    error       TEXT,
    log_path    TEXT,
    patch_path  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

-- "oldest queued task" is what workers ask for most often (M4).
CREATE INDEX tasks_state_created_idx ON tasks (state, created_at);
