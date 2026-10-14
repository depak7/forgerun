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

Be concise. Do not change any files.`

// Publisher turns a successful task's changes into a pull request.
// publish.Publisher implements it; it runs on the host, never in a VM.
type Publisher interface {
	Publish(ctx context.Context, req PublishRequest) (PublishedPR, error)
}

// PublishedPR is the pull request a task opened or updated.
type PublishedPR struct {
	URL    string
	Number int
	Branch string
}

// PublishRequest is what a pull request is made from.
type PublishRequest struct {
	TaskID     string
	Repo       string
	BaseCommit string // the commit the task started from
	PatchPath  string // the changes, as saved by the runner
	Prompt     string
	Summary    string   // "" if the agent couldn't give one
	Requests   []string // every request in the thread, oldest first, this one last
	// For a follow-up: the existing branch and pull request to update.
	// Branch "" means open a new one.
	Branch   string
	PRNumber int
}
