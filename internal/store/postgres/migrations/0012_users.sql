-- Accounts: people sign in with GitHub. A login is one signed-in browser;
-- only a hash of its cookie is stored, so this table can't be replayed.
CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    github_id     BIGINT NOT NULL UNIQUE,
    login         TEXT   NOT NULL,  -- GitHub username, e.g. "depak7"
    name          TEXT,
    avatar_url    TEXT,
    github_token  BYTEA,            -- their OAuth token, encrypted (AES-GCM, FORGERUN_SECRET_KEY)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE logins (
    id         TEXT PRIMARY KEY,   -- sha256 of the cookie value, hex
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);

-- Who started each task; NULL for tasks from before accounts (and in
-- local mode, without sign-in), which only an admin sees.
ALTER TABLE tasks ADD COLUMN owner_id BIGINT REFERENCES users (id);
CREATE INDEX tasks_owner_created ON tasks (owner_id, created_at DESC);
