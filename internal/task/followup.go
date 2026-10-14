package task

import (
	"context"
	"fmt"
	"strings"

	"github.com/depak7/forgerun/internal/sandbox"
)

// followUpSummaryNote is added to SummaryPrompt for follow-ups, so the new
// summary replaces the old one: it covers the whole thread.
const followUpSummaryNote = "Cover the whole pull request, including the earlier work described at the start of this conversation."

// prompt is what the agent is asked. For a follow-up, the thread's context
// comes first and the user's new request last.
func prompt(t Task) string {
	fu := t.FollowUp
	if fu == nil {
		return t.Prompt
	}
	var b strings.Builder
	b.WriteString("You are continuing work on an open pull request.\n\n")
	if fu.Summary != "" {
		b.WriteString("Summary of the work so far:\n\n" + fu.Summary + "\n\n")
	} else if len(fu.Requests) > 0 {
		b.WriteString("Earlier requests in this pull request, oldest first:\n\n")
		for i, r := range fu.Requests {
			fmt.Fprintf(&b, "%d. %s\n", i+1, r)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "The checked-out branch %s already contains that work; `git diff %s HEAD` shows all of it.\n\n", fu.Branch, fu.ThreadBase)
	b.WriteString("New request:\n\n" + t.Prompt)
	return b.String()
}

// checkoutThread switches the checkout to the thread's pull request branch.
// The thread's start commit is fetched too, so the agent can diff against it
// (shallow clones don't share history to compute a merge base from).
func checkoutThread(ctx context.Context, sb *sandbox.Sandbox, fu *FollowUp) error {
	git := func(args ...string) error {
		_, err := sb.Run(ctx, sandbox.Cmd{Name: "git", Args: append([]string{"-C", repoDir}, args...)})
		return err
	}
	if err := git("fetch", "-q", "--depth=1", "origin", fu.ThreadBase); err != nil {
		return fmt.Errorf("fetching the thread's start commit: %w", err)
	}
	if err := git("fetch", "-q", "--depth=1", "origin", fu.Branch); err != nil {
		return fmt.Errorf("fetching branch %s: %w", fu.Branch, err)
	}
	if err := git("checkout", "-q", "-B", fu.Branch, "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checking out %s: %w", fu.Branch, err)
	}
	return nil
}
