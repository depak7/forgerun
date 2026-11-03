-- Personal tokens: they act as their user from tools without a browser
-- (forgerun's MCP server). Only a hash of each is stored, like logins.
CREATE TABLE api_tokens (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    hash         TEXT NOT NULL UNIQUE, -- sha256 of the token, hex
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);
CREATE INDEX api_tokens_user ON api_tokens (user_id);
