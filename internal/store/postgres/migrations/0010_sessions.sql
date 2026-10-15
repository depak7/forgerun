-- Sessions: a thread keeps its VM between turns, and nothing is pushed
-- until the user asks. Each turn keeps what the agent said and whether its
-- changes have been pushed yet.
ALTER TABLE tasks
    ADD COLUMN reply  TEXT,                          -- the end of the agent's output
    ADD COLUMN pushed BOOLEAN NOT NULL DEFAULT false; -- its changes are on the branch
