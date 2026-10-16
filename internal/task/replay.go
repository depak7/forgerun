package task

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/depak7/forgerun/internal/sandbox"
)

// maxReply is how much of an agent's output is kept as its reply: the end,
// where text-mode agents print their answer.
const maxReply = 16 << 10

// maxReplayReply bounds each earlier reply in a replayed conversation, so
// a long thread still fits in a prompt.
const maxReplayReply = 2000

// replayPrompt is the prompt for an agent that doesn't have the thread's
// conversation (its session was rebuilt, or the thread changed agent):
// the earlier messages and replies, then the new message.
func replayPrompt(history []Turn, prompt string) string {
	var b strings.Builder
	b.WriteString("You are continuing a conversation with a user about this repository. You don't have the earlier chat, so here it is, oldest first.\n\n")
	for i, h := range history {
		fmt.Fprintf(&b, "--- Message %d from the user:\n%s\n\n", i+1, h.Prompt)
		switch {
		case h.Failed:
			b.WriteString("--- (That turn failed; its changes were discarded.)\n\n")
		case h.Reply != "" && h.Plan:
			fmt.Fprintf(&b, "--- Your plan (no files were changed):\n%s\n\n", clip(h.Reply, maxReplayReply))
		case h.Reply != "":
			fmt.Fprintf(&b, "--- Your reply:\n%s\n\n", clip(h.Reply, maxReplayReply))
		}
	}
	b.WriteString("The working tree already has every change made so far, one commit per turn (`git log` shows them).\n\n")
	b.WriteString("--- New message from the user:\n" + prompt)
	return b.String()
}

// clip keeps the end of s, where an agent's answer is.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// rebuild puts a new session's checkout where the thread left off: the
// thread's start commit, then each earlier turn's changes as a commit.
func rebuild(ctx context.Context, sb *sandbox.Sandbox, t Task, history []Turn) error {
	git := func(args ...string) error {
		_, err := sb.Run(ctx, sandbox.Cmd{Name: "git", Args: append([]string{"-C", repoDir}, args...)})
		return err
	}
	base := t.ThreadBase
	if t.Fetch != nil {
		if _, err := HostFetch(ctx, sb, t.Fetch, t.Repo, base, repoDir); err != nil {
			return fmt.Errorf("fetching the thread's start commit: %w", err)
		}
	} else if err := git("fetch", "-q", "--depth=1", "origin", base); err != nil {
		return fmt.Errorf("fetching the thread's start commit: %w", err)
	}
	if err := git("checkout", "-q", "-B", "forgerun-session", base); err != nil {
		return fmt.Errorf("checking out the thread's start commit: %w", err)
	}
	const patch = "/tmp/forgerun-turn.patch"
	for i, h := range history {
		if h.Diff == "" {
			continue
		}
		if err := sb.WriteFile(ctx, patch, []byte(h.Diff)); err != nil {
			return err
		}
		if err := git("apply", "--index", patch); err != nil {
			return fmt.Errorf("re-applying turn %d's changes: %w", i+1, err)
		}
		if err := git("-c", "user.name=forgerun", "-c", "user.email=forgerun@localhost", "commit", "-q", "--no-verify", "-m", fmt.Sprintf("turn %d", i+1)); err != nil {
			return fmt.Errorf("committing turn %d: %w", i+1, err)
		}
	}
	return nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
