-- Snapshots: a VM disk with a repo cloned and its dependencies installed, so
-- tasks can restore it instead of cloning and installing from scratch.
-- Rows are never deleted, only marked 'deleted', so tasks can still say
-- which snapshot they started from.
CREATE TABLE snapshots (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo             TEXT NOT NULL,  -- normalized repo URL
    agent_id         TEXT NOT NULL REFERENCES agents (id),
    ref              TEXT NOT NULL,  -- microsandbox snapshot reference
    commit_sha       TEXT NOT NULL,  -- the commit the repo was reset to
    deps_fingerprint TEXT NOT NULL DEFAULT '',
    size_bytes       BIGINT NOT NULL DEFAULT 0,  -- real disk usage
    state            TEXT NOT NULL DEFAULT 'current' CHECK (state IN ('current', 'retired', 'deleted')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ
);

-- At most one current snapshot per repo and agent.
CREATE UNIQUE INDEX snapshots_one_current_idx ON snapshots (repo, agent_id) WHERE state = 'current';

-- The snapshot a task was restored from; NULL = cold start.
ALTER TABLE tasks ADD COLUMN snapshot_id BIGINT REFERENCES snapshots (id);
