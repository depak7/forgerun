package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/depak7/forgerun/internal/mcp"
)

// approvePrompt approves a thread's plan: the message the web UI's
// "approve & build" sends.
const approvePrompt = "Approved. Go ahead and implement the plan."

// orchestrateTools let any agent run forgerun threads on GitHub repos:
// start them, follow them, review and steer them, and push the result.
func orchestrateTools(c *Client) []mcp.Tool {
	return []mcp.Tool{
		{
			Name:        "forgerun_agents",
			Description: "The coding agents forgerun can run and whether each has its key; with agent, that agent's models and their efforts. Use it to choose agent, model and effort for forgerun_start and forgerun_followup.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {"agent": {"type": "string", "description": "An agent's ID, to list its models."}}
}`),
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				var a struct{ Agent string }
				json.Unmarshal(args, &a)
				if a.Agent != "" {
					return c.models(ctx, a.Agent)
				}
				return c.agents(ctx)
			},
		},
		{
			Name:        "forgerun_branches",
			Description: "A GitHub repo's default branch and up to 100 of its branches, to start a thread from.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {"repo": {"type": "string", "description": "The repo's URL, e.g. https://github.com/owner/name."}},
  "required": ["repo"]
}`),
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				var a struct{ Repo string }
				if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.Repo) == "" {
					return "", errors.New("repo is required")
				}
				def, names, err := c.Branches(ctx, strings.TrimSpace(a.Repo))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Default branch: %s\nBranches (%d): %s", def, len(names), strings.Join(names, ", ")), nil
			},
		},
		{
			Name: "forgerun_start",
			Description: `Start a forgerun thread: a coding agent works on a GitHub repo in its own sandbox, on the user's forgerun workers. Nothing reaches GitHub until forgerun_push.

Write the prompt to stand alone (the agent sees only it and the repo): what to do, constraints, and how to check the work. With plan, the agent first proposes a plan and waits for forgerun_followup to approve it.

Returns the thread's ID. Follow it with forgerun_status (use wait), review with forgerun_diff, steer with forgerun_followup.`,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "repo": {"type": "string", "description": "The repo's URL, e.g. https://github.com/owner/name."},
    "prompt": {"type": "string", "description": "What the agent should do."},
    "branch": {"type": "string", "description": "Branch to start from; its pull request goes into it. Omit for the repo's default branch."},
    "agent": {"type": "string", "description": "forgerun agent, e.g. claude-code, cursor, opencode, pi, prime (see forgerun_agents). Omit for the server's default."},
    "model": {"type": "string", "description": "Model for that agent, as forgerun_agents lists it. Omit for the agent's default."},
    "effort": {"type": "string", "description": "Reasoning effort, e.g. low, medium, high. Omit for the default."},
    "plan": {"type": "boolean", "description": "Plan first: the agent proposes a plan, changes nothing, and waits for approval."}
  },
  "required": ["repo", "prompt"]
}`),
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				var s Start
				if err := json.Unmarshal(args, &s); err != nil {
					return "", err
				}
				s.Repo, s.Prompt, s.Folder = strings.TrimSpace(s.Repo), strings.TrimSpace(s.Prompt), ""
				if s.Repo == "" || s.Prompt == "" {
					return "", errors.New("repo and prompt are required")
				}
				t, err := c.StartThread(ctx, s)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Started thread %s on %s (%s, model %s). %s\nFollow it with forgerun_status (wait), review with forgerun_diff.",
					t.ThreadID, where(t), t.Agent, t.ModelID, c.ThreadURL(t.ThreadID)), nil
			},
		},
		{
			Name:        "forgerun_threads",
			Description: "The user's forgerun threads, most recent activity first: each one's state, and whether it needs attention (failed, or waiting for an answer or approval).",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {"limit": {"type": "integer", "description": "How many, 1 to 100; default 20."}}
}`),
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				var a struct{ Limit int }
				json.Unmarshal(args, &a)
				limit := 20
				if a.Limit > 0 {
					limit = min(a.Limit, 100)
				}
				ts, err := c.Threads(ctx, limit)
				if err != nil {
					return "", err
				}
				if len(ts) == 0 {
					return "No threads yet.", nil
				}
				var b strings.Builder
				for _, t := range ts {
					attention := ""
					if t.NeedsAttention {
						attention = " · NEEDS ATTENTION"
					}
					fmt.Fprintf(&b, "%s · %s%s · %s · %d turn(s) · %s\n  %s\n", t.ID, t.Latest.State, attention, where(t.Latest), t.Steps, t.UpdatedAt, firstLine(t.Title))
				}
				return b.String(), nil
			},
		},
		{
			Name:        "forgerun_log",
			Description: "The end of the log of a thread's latest turn: what the agent is doing or did (its words, commands and their output). Use it to follow a running turn or see why one failed.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "thread": {"type": "string", "description": "The thread's ID, or any of its turns'."},
    "bytes": {"type": "integer", "description": "How much of the end to return, at most 65536; default 8000."}
  },
  "required": ["thread"]
}`),
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				id, err := threadArg(args)
				if err != nil {
					return "", err
				}
				var a struct{ Bytes int }
				json.Unmarshal(args, &a)
				_, last, err := c.ends(ctx, id)
				if err != nil {
					return "", err
				}
				n := 8000
				if a.Bytes > 0 {
					n = min(a.Bytes, 64<<10)
				}
				return c.Log(ctx, last.ID, n)
			},
		},
		{
			Name: "forgerun_followup",
			Description: `The thread's next message, carried on in its session: ask for changes after reviewing the diff, answer the agent's questions, or approve its plan (approve). It may switch agent or model.

Only one turn runs at a time: wait for the latest to finish (forgerun_status with wait) first.`,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "thread": {"type": "string", "description": "The thread's ID, or any of its turns'."},
    "prompt": {"type": "string", "description": "The message. Optional with approve."},
    "approve": {"type": "boolean", "description": "Approve the agent's plan and have it build it."},
    "agent": {"type": "string", "description": "Switch to this agent. Omit to keep the thread's."},
    "model": {"type": "string", "description": "Model for the agent. Omit for its default."},
    "effort": {"type": "string", "description": "Reasoning effort. Omit for the default."},
    "plan": {"type": "boolean", "description": "Plan only: the agent revises its plan and changes nothing."}
  },
  "required": ["thread"]
}`),
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				id, err := threadArg(args)
				if err != nil {
					return "", err
				}
				var a struct {
					Start
					Approve bool
				}
				if err := json.Unmarshal(args, &a); err != nil {
					return "", err
				}
				s := a.Start
				s.Repo, s.Branch, s.Folder = "", "", "" // a follow-up stays on the thread's
				s.Prompt = strings.TrimSpace(s.Prompt)
				if a.Approve {
					s.Prompt = strings.TrimSpace(approvePrompt + "\n\n" + s.Prompt)
				}
				if s.Prompt == "" {
					return "", errors.New("prompt is required (or approve)")
				}
				_, last, err := c.ends(ctx, id)
				if err != nil {
					return "", err
				}
				t, err := c.FollowUp(ctx, last.ID, s)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Sent: turn %s of thread %s (%s, model %s) is %s. Follow it with forgerun_status (wait).", t.ID, t.ThreadID, t.Agent, t.ModelID, t.State), nil
			},
		},
		{
			Name:        "forgerun_cancel",
			Description: "Stop a thread's latest turn if it's queued or running. Its changes are discarded; the thread can go on with forgerun_followup.",
			InputSchema: threadSchema,
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				id, err := threadArg(args)
				if err != nil {
					return "", err
				}
				_, last, err := c.ends(ctx, id)
				if err != nil {
					return "", err
				}
				t, err := c.Cancel(ctx, last.ID)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Turn %s is %s.", t.ID, t.State), nil
			},
		},
		{
			Name: "forgerun_push",
			Description: `Push a thread's changes to GitHub, as the user: one commit on its forgerun/… branch, and with pr a pull request (into the branch the thread started from), described by the agent's summary of the thread. Later pushes add to the same branch and pull request.

Review the changes with forgerun_diff first: this is what reaches the user's repo.`,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "thread": {"type": "string", "description": "The thread's ID, or any of its turns'."},
    "pr": {"type": "boolean", "description": "Open a pull request too (if the thread has none yet)."}
  },
  "required": ["thread"]
}`),
			Call: func(ctx context.Context, args json.RawMessage) (string, error) {
				id, err := threadArg(args)
				if err != nil {
					return "", err
				}
				var a struct{ PR bool }
				json.Unmarshal(args, &a)
				first, _, err := c.ends(ctx, id)
				if err != nil {
					return "", err
				}
				ts, err := c.Push(ctx, first.ThreadID, a.PR)
				if err != nil {
					return "", err
				}
				last := ts[len(ts)-1]
				switch {
				case last.PR.URL != "":
					return "Pushed to " + last.PR.Branch + ". Pull request: " + last.PR.URL, nil
				case last.PR.Error != "":
					return "Pushed to " + last.PR.Branch + ", but the pull request failed: " + last.PR.Error, nil
				}
				return "Pushed to " + last.PR.Branch + ".", nil
			},
		},
	}
}

// ends returns a thread's first and latest turns; id is any turn's.
func (c *Client) ends(ctx context.Context, id string) (first, last Task, err error) {
	turns, err := c.Thread(ctx, id)
	if err != nil {
		return Task{}, Task{}, err
	}
	if len(turns) == 0 {
		return Task{}, Task{}, fmt.Errorf("thread %s has no turns", id)
	}
	return turns[0], turns[len(turns)-1], nil
}

func (c *Client) agents(ctx context.Context) (string, error) {
	as, err := c.Agents(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, a := range as {
		key := "needs no key"
		switch {
		case !a.Runnable:
			key = "not available on this server"
		case a.Credential == nil:
		case a.Credential.Set:
			key = "key set"
		case a.Credential.Optional:
			key = "no key: only its free models run"
		default:
			key = "no key (" + a.Credential.Name + "): its tasks are refused"
		}
		fmt.Fprintf(&b, "%s (%s): %s; default model %s\n", a.ID, a.Name, key, a.DefaultModel)
	}
	b.WriteString("forgerun_agents with agent lists an agent's models.")
	return b.String(), nil
}

func (c *Client) models(ctx context.Context, agent string) (string, error) {
	ms, err := c.Models(ctx, agent)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s's models (pass the ID as model):\n", agent)
	for _, m := range ms {
		fmt.Fprintf(&b, "%s · %s", m.ID, m.Name)
		if len(m.Efforts) > 0 {
			fmt.Fprintf(&b, " · efforts: %s", strings.Join(m.Efforts, ", "))
		}
		if m.Keyless {
			b.WriteString(" · free, no key")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return cut(s, 100)
}

// cut keeps the first n runes of s.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
