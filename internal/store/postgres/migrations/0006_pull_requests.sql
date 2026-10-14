-- Pull requests: a task can ask for one; the outcome records what happened.
ALTER TABLE tasks
    ADD COLUMN pr_requested BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN base_commit  TEXT,  -- the commit the agent started from
    ADD COLUMN summary      TEXT,  -- the agent's conversation summary (PR tasks)
    ADD COLUMN pr_url       TEXT,  -- the pull request, if one was opened
    ADD COLUMN pr_error     TEXT;  -- why a requested pull request wasn't opened
