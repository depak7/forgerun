-- Copies of local folders (or single files), uploaded to work on instead of
-- a GitHub repo. A thread's tasks point at the folder it started from.
CREATE TABLE folders (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    owner_id   BIGINT REFERENCES users (id),
    data       BYTEA NOT NULL, -- gzipped tar of the files (with .git for a repo)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE tasks ADD COLUMN folder_id TEXT REFERENCES folders (id);
