-- Threads: a follow-up task continues an earlier task's pull request.
-- thread_id is the thread's first task (a task alone is its own thread).
ALTER TABLE tasks
    ADD COLUMN parent_id TEXT REFERENCES tasks (id),  -- the task this one continues
    ADD COLUMN thread_id TEXT REFERENCES tasks (id),
    ADD COLUMN pr_number INTEGER,                       -- the pull request, once opened
    ADD COLUMN pr_branch TEXT;
UPDATE tasks SET thread_id = id;
ALTER TABLE tasks ALTER COLUMN thread_id SET NOT NULL;
CREATE INDEX tasks_thread_idx ON tasks (thread_id, created_at);

-- A thread is a straight line: each task has at most one follow-up...
CREATE UNIQUE INDEX tasks_one_followup_idx ON tasks (parent_id);
-- ...and runs one task at a time.
CREATE UNIQUE INDEX tasks_one_active_per_thread_idx ON tasks (thread_id) WHERE state IN ('queued', 'running');
