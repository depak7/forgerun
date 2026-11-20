-- Signed-in browsers. Only a hash of each login cookie is stored, so this
-- table can't be replayed.
CREATE TABLE logins (
    id         TEXT PRIMARY KEY,   -- sha256 of the cookie value, hex
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
