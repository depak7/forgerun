package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/depak7/forgerun/internal/mcp"
)

// handoffPreamble opens a handed-off thread's first message, for the agent
// that carries on.
const handoffPreamble = `You're carrying on work handed off from a coding session on the user's machine. The repo here is their folder as it was at the handoff, uncommitted work included. Here is that session's summary:

`

// Tools are the MCP tools a local agent hands work off with.
func Tools(c *Client) []mcp.Tool {
	return []mcp.Tool{
		{
			Name: "forgerun_handoff",
			Description: `Hand the rest of the current task to forgerun, which carries it on in a sandbox on the user's own workers. Use it when the user asks to hand off, delegate, or continue the work in the cloud or in forgerun.

It sends a copy of the folder (in a git repo: the repo, as it is now, uncommitted work included; what .gitignore ignores and untracked secret files like .env stay behind), and starts a thread whose first message is your summary. The agent there sees only that summary and the files, so write it to stand alone:
- Requested: what the user asked for, in their terms, with every constraint they gave.
- Done: what has been done and decided so far, and why where it matters.
- Left: what remains, as concrete next steps, and how to check the work (tests to run).
Name files and functions. Don't paste code that's already in the folder.

Returns the thread's ID and link. Check on it later with forgerun_status, and fetch its changes with forgerun_diff.`,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Absolute path of the folder (or one file) to hand off: usually the project you're working in."},
    "summary": {"type": "string", "description": "The handoff summary: Requested, Done, Left (see the tool's description)."},
    "agent": {"type": "string", "description": "forgerun agent to carry on with, e.g. claude-code, cursor, opencode, pi. Omit for the server's default."},
    "model": {"type": "string", "description": "Model for that agent, as forgerun names it. Omit for the agent's default."},
    "effort": {"type": "string", "description": "Reasoning effort, e.g. low, medium, high. Omit for the default."},
    "plan": {"type": "boolean", "description": "Plan first: the agent proposes a plan and waits for the user's approval in forgerun."}
  },
  "required": ["path", "summary"]
}`),
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				var a struct {
					Path, Summary, Agent, Model, Effort string
					Plan                                bool
				}
				if err := json.Unmarshal(args, &a); err != nil {
					return "", err
				}
				if strings.TrimSpace(a.Path) == "" || strings.TrimSpace(a.Summary) == "" {
					return "", errors.New("path and summary are required")
				}
				return c.handoff(ctx, a.Path, Start{Prompt: handoffPreamble + strings.TrimSpace(a.Summary), Agent: a.Agent, Model: a.Model, Effort: a.Effort, Plan: a.Plan})
			},
		},
		{
			Name:        "forgerun_status",
			Description: "How a handed-off forgerun thread is doing: whether its latest turn is running, finished, failed, or waiting for the user (questions or a plan to approve), and what the agent said.",
			InputSchema: threadSchema,
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				id, err := threadArg(args)
				if err != nil {
					return "", err
				}
				return c.status(ctx, id)
			},
		},
		{
			Name:        "forgerun_diff",
			Description: "The changes a handed-off forgerun thread made, as one patch: every finished turn's changes, oldest first. Apply it in the folder that was handed off with `git apply` (check first with `git apply --check`).",
			InputSchema: threadSchema,
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				id, err := threadArg(args)
				if err != nil {
					return "", err
				}
				return c.threadDiff(ctx, id)
			},
		},
	}
}

var threadSchema = json.RawMessage(`{
  "type": "object",
  "properties": {"thread": {"type": "string", "description": "The thread's ID (forgerun_handoff returned it), or any of its turns'."}},
  "required": ["thread"]
}`)

func threadArg(args json.RawMessage) (string, error) {
	var a struct{ Thread string }
	if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.Thread) == "" {
		return "", errors.New("thread is required")
	}
	return strings.TrimSpace(a.Thread), nil
}

func (c *Client) handoff(ctx context.Context, path string, s Start) (string, error) {
	pk, err := Pack(ctx, path)
	if err != nil {
		return "", fmt.Errorf("packing %s: %w", path, err)
	}
	folder, err := c.Upload(ctx, pk)
	if err != nil {
		return "", err
	}
	t, err := c.StartThread(ctx, folder, s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Handed off to forgerun: thread %s (%s, model %s).\n%s\n\n", t.ThreadID, t.Agent, t.ModelID, c.ThreadURL(t.ThreadID))
	fmt.Fprintf(&b, "Sent %s: %d file(s), %d KB packed.", pk.Root, pk.Files, (len(pk.Data)+1023)/1024)
	if pk.Base != "" {
		fmt.Fprintf(&b, " It starts from commit %.12s", pk.Base)
		if pk.Branch != "" {
			fmt.Fprintf(&b, " on %s", pk.Branch)
		}
		b.WriteString(", with the uncommitted work on top.")
	}
	if len(pk.Skipped) > 0 {
		fmt.Fprintf(&b, "\nLeft out as secrets: %s.", strings.Join(pk.Skipped, ", "))
	}
	b.WriteString("\nCheck on it with forgerun_status; get its changes with forgerun_diff.")
	return b.String(), nil
}

func (c *Client) status(ctx context.Context, id string) (string, error) {
	turns, err := c.Thread(ctx, id)
	if err != nil {
		return "", err
	}
	if len(turns) == 0 {
		return "", fmt.Errorf("thread %s has no turns", id)
	}
	last := turns[len(turns)-1]
	var b strings.Builder
	fmt.Fprintf(&b, "Thread %s on %s: %d turn(s). %s\n", last.ThreadID, last.Repo, len(turns), c.ThreadURL(last.ThreadID))
	switch {
	case last.State == "queued" || last.State == "running":
		fmt.Fprintf(&b, "The latest turn is %s (%s, model %s).", last.State, last.Agent, last.ModelID)
	case last.State == "failed" || last.State == "cancelled":
		fmt.Fprintf(&b, "The latest turn %s: %s", last.State, last.Error)
	case last.Plan:
		b.WriteString("The agent wrote a plan and waits for the user to approve it in forgerun.")
	case last.Asked:
		b.WriteString("The agent asked the user questions; answer them in forgerun.")
	default:
		b.WriteString("The latest turn finished.")
	}
	if changed := slicesCount(turns, func(t Task) bool { return t.State == "succeeded" && t.HasDiff && !t.Plan }); changed > 0 {
		fmt.Fprintf(&b, " %d turn(s) changed files: forgerun_diff gets the changes.", changed)
	}
	if last.PR.URL != "" {
		fmt.Fprintf(&b, "\nPull request: %s", last.PR.URL)
	} else if last.PR.Branch != "" {
		fmt.Fprintf(&b, "\nBranch: %s", last.PR.Branch)
	}
	if last.Reply != "" {
		fmt.Fprintf(&b, "\n\nThe agent's last words:\n%s", last.Reply)
	}
	return b.String(), nil
}

func (c *Client) threadDiff(ctx context.Context, id string) (string, error) {
	turns, err := c.Thread(ctx, id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range turns {
		if t.State != "succeeded" || !t.HasDiff || t.Plan {
			continue
		}
		d, err := c.Diff(ctx, t.ID)
		if err != nil {
			return "", err
		}
		b.WriteString(d)
		if !strings.HasSuffix(d, "\n") {
			b.WriteString("\n")
		}
	}
	if b.Len() == 0 {
		return "The thread hasn't changed any files yet.", nil
	}
	return b.String(), nil
}

func slicesCount[T any](s []T, f func(T) bool) int {
	n := 0
	for _, v := range s {
		if f(v) {
			n++
		}
	}
	return n
}
