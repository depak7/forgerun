-- Plan-only turns, and turns where the agent asked the user questions
-- instead of going on.
ALTER TABLE tasks
    ADD COLUMN plan  BOOLEAN NOT NULL DEFAULT false, -- read-only: the reply is a plan
    ADD COLUMN asked BOOLEAN NOT NULL DEFAULT false; -- the reply ends with questions for the user
