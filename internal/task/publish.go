package task

import (
	"context"
)

// SummaryPrompt is what forgerun asks an agent at the end of a pull request
// task, by continuing its own conversation. The same words go to every
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

// Publisher pushes a successful task's changes to a branch, and opens or
// updates a pull request when one is wanted. publish.Publisher implements
// it; it runs on the host, never in a VM.
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
	TaskID     string
	Repo       string
	BaseCommit string // the commit the task started from
	Patch      string // the changes, as git diff --binary output
	Prompt     string
	Summary    string   // "" if the agent couldn't give one
	Requests   []string // every request in the thread, oldest first, this one last
	OpenPR     bool     // open a pull request if the thread has none yet
	// For a follow-up: the thread's branch, and its pull request (0 = none
	// yet). Branch "" means a new thread: a new branch is made.
	Branch   string
	PRNumber int
}
