-- A task's changes are kept in the database, not as a file on one host's
-- disk: the diff is shown in the UI and every task pushes it to a branch.
ALTER TABLE tasks
    ADD COLUMN diff TEXT,  -- git diff --binary of the changes; NULL if none
    DROP COLUMN patch_path;
