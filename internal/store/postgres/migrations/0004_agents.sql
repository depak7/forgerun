-- Agents forgerun can run (cursor, later claude-code, ...). Models and tasks
-- belong to an agent, and a model id is only unique within its agent.
CREATE TABLE agents (
    id               TEXT PRIMARY KEY,  -- slug used everywhere: 'cursor', 'claude-code'
    name             TEXT NOT NULL,     -- display name
    enabled          BOOLEAN NOT NULL DEFAULT true,
    default_model_id TEXT,              -- used when a request names no model
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO agents (id, name) VALUES ('cursor', 'Cursor');

-- Models: keyed per agent.
ALTER TABLE tasks DROP CONSTRAINT tasks_model_id_fkey;
ALTER TABLE tasks ALTER COLUMN model_id DROP DEFAULT;
ALTER TABLE models DROP CONSTRAINT models_pkey;
ALTER TABLE models RENAME COLUMN agent TO agent_id;
ALTER TABLE models ADD PRIMARY KEY (agent_id, id);
ALTER TABLE models ADD CONSTRAINT models_agent_fkey FOREIGN KEY (agent_id) REFERENCES agents (id);

-- Tasks: belong to an agent and use one of that agent's models.
ALTER TABLE tasks RENAME COLUMN agent TO agent_id;
ALTER TABLE tasks ADD CONSTRAINT tasks_agent_fkey FOREIGN KEY (agent_id) REFERENCES agents (id);
ALTER TABLE tasks ADD CONSTRAINT tasks_model_fkey
    FOREIGN KEY (agent_id, model_id) REFERENCES models (agent_id, id);

-- An agent's default must be one of its own models.
ALTER TABLE agents ADD CONSTRAINT agents_default_model_fkey
    FOREIGN KEY (id, default_model_id) REFERENCES models (agent_id, id);
UPDATE agents SET default_model_id = 'auto' WHERE id = 'cursor';
