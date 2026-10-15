package task

import (
	"context"
)

// SummaryPrompt is what forgerun asks an agent before pushing a thread with
// a pull request, by continuing its own conversation. The same words go to every
// agent. The summary becomes the pull request's description, and follow-ups
// will carry it forward as context.
const SummaryPrompt = `Summarize this whole conversation for a pull request description. Use Markdown with exactly three sections:

## Requested
What the user asked for, including every detail, constraint and preference they gave.

## Done
What you changed and why, which files, and how you verified it (commands you ran and their results).

## Open
Assumptions you made, open questions, and anything not done.

Be concise. Do not change any files. forgerun commits your changes and opens or updates the pull request itself, so don't mention committing or pushing.`

// Publisher pushes a thread's unpushed changes to its branch as one
// commit, and opens or updates its pull request when one is wanted.
// publish.Publisher implements it; it runs on the host, never in a VM.
type Publisher interface {
	Publish(ctx context.Context, req PublishRequest) (Published, error)
}

// Published is where a task's changes went: always a branch, and a pull
// request when one was opened or updated (PRNumber 0 = none).
type Published struct {
	Branch   string
	PRURL    string
	PRNumber int
}

// PublishRequest is what a push (and maybe a pull request) is made from.
type PublishRequest struct {
	ThreadID string
	Repo     string
	// BaseCommit is the commit the thread started from: a new branch is
	// built on it. An existing branch is built on its own head instead.
	BaseCommit string
	// Patches are the turns' unpushed changes (git diff --binary), oldest
	// first; they're applied in order and pushed as one commit. None =
	// nothing to push (e.g. only opening a pull request for the branch).
	Patches  []string
	Summary  string   // "" if the agent couldn't give one
	Requests []string // every request in the thread, oldest first
	OpenPR   bool     // open a pull request if the thread has none yet
	// The thread's branch ("" = none yet: one is made) and pull request
	// (0 = none yet).
	Branch   string
	PRNumber int
}
