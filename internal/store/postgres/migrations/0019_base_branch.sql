-- The branch a repo's thread started from, on every turn of the thread
-- (follow-ups inherit it); NULL = the repo's default branch.
ALTER TABLE tasks ADD COLUMN base_branch TEXT;
