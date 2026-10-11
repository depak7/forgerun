-- Models a task can run on. Adding a model is an INSERT; switching one off is
-- UPDATE ... SET enabled = false. The structured columns (family, effort,
-- thinking, fast) are stored, never parsed from the id at runtime.
CREATE TABLE models (
    id         TEXT PRIMARY KEY,               -- exact agent model id: 'claude-opus-5-5-high-fast'
    agent      TEXT NOT NULL,                  -- 'cursor'
    family     TEXT NOT NULL,                  -- 'claude-opus-5-5'
    name       TEXT NOT NULL,                  -- display name
    effort     TEXT NOT NULL DEFAULT '',       -- 'high'; '' = the family's plain variant
    thinking   BOOLEAN NOT NULL DEFAULT false,
    fast       BOOLEAN NOT NULL DEFAULT false,
    is_default BOOLEAN NOT NULL DEFAULT false, -- used when a request omits effort
    zdr        BOOLEAN NOT NULL DEFAULT true,  -- false: provider may retain prompts and code
    enabled    BOOLEAN NOT NULL DEFAULT true,
    UNIQUE (agent, family, effort, thinking, fast)
);

-- Auto lets the agent pick; it must exist before tasks can reference it.
INSERT INTO models (id, agent, family, name, is_default) VALUES ('auto', 'cursor', 'auto', 'Auto', true);

-- Every existing task ran on Auto.
ALTER TABLE tasks ADD COLUMN model_id TEXT NOT NULL DEFAULT 'auto' REFERENCES models (id);
