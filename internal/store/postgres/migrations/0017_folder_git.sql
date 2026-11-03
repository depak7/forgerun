-- A folder from a GitHub repo can be pushed: its repo, the commit it was
-- on and its branch, and its uncommitted work at handoff as a diff (which
-- the first turn's VM reports), the first change pushed on that commit.
ALTER TABLE folders
    ADD COLUMN repo         TEXT, -- https://github.com/owner/name; NULL if not a GitHub repo
    ADD COLUMN base_commit  TEXT,
    ADD COLUMN branch       TEXT,
    ADD COLUMN handoff_diff TEXT; -- git diff --binary of the uncommitted work; NULL until known
