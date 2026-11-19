package handoff

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/api"
	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/mcp"
	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
)

type nopPool struct{}

func (nopPool) Nudge()                {}
func (nopPool) Cancel(id string) bool { return false }

// fakeGitHub is GitHub for a server that pushes: every repo can be pushed
// to and has branches main (its default) and dev; pushes are recorded.
type fakeGitHub struct{ pushed *[]task.PublishRequest }

func (fakeGitHub) IsOpen(context.Context, string, int, forge.Token) (bool, error) { return true, nil }
func (fakeGitHub) Access(context.Context, string, forge.Token) (forge.Access, error) {
	return forge.Access{Push: true}, nil
}
func (fakeGitHub) Repos(context.Context, forge.Token) ([]forge.RepoInfo, error) { return nil, nil }
func (fakeGitHub) HasBranch(_ context.Context, _, b string, _ forge.Token) (bool, error) {
	return b == "main" || b == "dev", nil
}
func (fakeGitHub) Branches(context.Context, string, forge.Token) (string, []string, error) {
	return "main", []string{"dev", "main"}, nil
}
func (f fakeGitHub) Publish(_ context.Context, req task.PublishRequest) (task.Published, error) {
	*f.pushed = append(*f.pushed, req)
	return task.Published{Branch: "forgerun/fix-it-abc", PRURL: "https://github.com/o/r/pull/9", PRNumber: 9}, nil
}

// server is a forgerun API in local mode on a memory store.
func server(t *testing.T, prs ...api.PRs) (*Client, *memory.Store) {
	t.Helper()
	s := memory.New(models.Variant{ID: "auto", AgentID: "cursor", Family: "auto", Name: "Auto", IsDefault: true, AgentDefault: true, ZDR: true})
	cfg := api.Config{Store: s, Pool: nopPool{}, Agent: "cursor", Log: slog.New(slog.DiscardHandler),
		Agents: agent.NewSet(agent.StaticCredentials{"CURSOR_API_KEY": "k"}, &agent.Cursor{})}
	if len(prs) > 0 {
		cfg.PRs = prs[0]
	}
	h := api.New(cfg)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{URL: srv.URL}, s
}

func call(t *testing.T, tools []mcp.Tool, name, args string) (string, error) {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			return tl.Call(context.Background(), json.RawMessage(args))
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

// The whole handoff: pack a repo with uncommitted work, upload it, start a
// thread with the summary; then see its status and fetch its changes.
func TestHandoffTools(t *testing.T) {
	c, s := server(t)
	tools := Tools(c)
	src := t.TempDir()
	must(t, src, "init", "-q", "-b", "main")
	write(t, src, "app.go", "package app\n")
	commit(t, src, "one")
	write(t, src, "app.go", "package app\n// wip\n")
	write(t, src, ".env", "KEY=x\n")

	args, _ := json.Marshal(map[string]any{"path": filepath.Join(src), "title": " Fix  the\nthing ", "summary": "Requested: X. Done: Y. Left: Z."})
	out, err := call(t, tools, "forgerun_handoff", string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Handed off to forgerun: thread t-") || !strings.Contains(out, "/threads/t-") ||
		!strings.Contains(out, "on main, with the uncommitted work on top") || !strings.Contains(out, "Left out as secrets: .env.") {
		t.Errorf("handoff said:\n%s", out)
	}
	threads, _ := s.ListThreads(context.Background(), 10, 0)
	if len(threads) != 1 {
		t.Fatalf("%d threads", len(threads))
	}
	id := threads[0].ID
	rec, _ := s.Get(context.Background(), id)
	if rec.FolderID == "" || rec.Prompt != "Fix the thing\n\nRequested: X. Done: Y. Left: Z.\n\n"+handoffNote {
		t.Errorf("thread %+v", rec)
	}
	if f, _ := s.GetFolder(context.Background(), rec.FolderID); f.Name != filepath.Base(src) {
		t.Errorf("folder name %q", f.Name)
	}

	out, _ = call(t, tools, "forgerun_status", `{"thread":"`+id+`"}`)
	if !strings.Contains(out, "The latest turn is queued") {
		t.Errorf("status:\n%s", out)
	}
	if out, _ = call(t, tools, "forgerun_diff", `{"thread":"`+id+`"}`); out != "The thread hasn't changed any files yet." {
		t.Errorf("diff before: %q", out)
	}

	ctx := context.Background()
	s.Claim(ctx)
	s.Finish(ctx, id, store.Outcome{State: task.Succeeded, Diff: "diff --git a/app.go b/app.go\n+one\n", Reply: "Did it."})
	next := task.New(rec.Repo, "more")
	next.ParentID = id
	s.Create(ctx, next, "cursor")
	s.Claim(ctx)
	s.Finish(ctx, next.ID, store.Outcome{State: task.Succeeded, Diff: "diff --git a/b.go b/b.go\n+two\n", Reply: "And more."})

	out, _ = call(t, tools, "forgerun_status", `{"thread":"`+next.ID+`"}`)
	if !strings.Contains(out, "2 turn(s)") || !strings.Contains(out, "The latest turn finished. 2 turn(s) changed files") || !strings.HasSuffix(out, "And more.") {
		t.Errorf("status:\n%s", out)
	}
	if out, _ = call(t, tools, "forgerun_diff", `{"thread":"`+id+`"}`); out != "diff --git a/app.go b/app.go\n+one\ndiff --git a/b.go b/b.go\n+two\n" {
		t.Errorf("diff = %q", out)
	}
}

func TestHandoffErrors(t *testing.T) {
	c, _ := server(t)
	tools := Tools(c)
	if _, err := call(t, tools, "forgerun_handoff", `{"path":"/tmp","summary":"s"}`); err == nil {
		t.Error("no title: no error")
	}
	if _, err := call(t, tools, "forgerun_status", `{}`); err == nil {
		t.Error("no thread: no error")
	}
	_, err := call(t, tools, "forgerun_status", `{"thread":"t-nope"}`)
	if err == nil || !strings.Contains(err.Error(), "forgerun:") {
		t.Errorf("unknown thread: %v", err)
	}
	unreachable := Tools(&Client{URL: "http://127.0.0.1:1"})
	if _, err := call(t, unreachable, "forgerun_status", `{"thread":"t-1"}`); err == nil || !strings.Contains(err.Error(), "reaching forgerun") {
		t.Errorf("unreachable: %v", err)
	}
}

// An agent orchestrating a thread on a branch: choose an agent, start with
// a plan, wait for it, approve it, cancel, steer, read the log, push.
func TestOrchestrateTools(t *testing.T) {
	var pushed []task.PublishRequest
	c, s := server(t, fakeGitHub{&pushed})
	c.Poll = 10 * time.Millisecond
	tools, ctx := Tools(c), context.Background()
	mustCall := func(name, args string) string {
		t.Helper()
		out, err := call(t, tools, name, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	expect := func(out string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("want %q in:\n%s", w, out)
			}
		}
	}

	expect(mustCall("forgerun_agents", `{}`), "cursor (cursor): key set; default model auto")
	expect(mustCall("forgerun_agents", `{"agent":"cursor"}`), "auto · Auto")
	expect(mustCall("forgerun_branches", `{"repo":"https://github.com/o/r"}`), "Default branch: main", "dev, main")

	if _, err := call(t, tools, "forgerun_start", `{"repo":"https://github.com/o/r","prompt":"p","branch":"nope"}`); err == nil || !strings.Contains(err.Error(), `no branch "nope"`) {
		t.Errorf("missing branch: %v", err)
	}
	expect(mustCall("forgerun_start", `{"repo":"https://github.com/o/r","prompt":"Fix it","branch":"dev","plan":true}`), "Started thread t-", "https://github.com/o/r (from dev)")
	threads, _ := s.ListThreads(ctx, 10, 0)
	id := threads[0].ID
	if rec, _ := s.Get(ctx, id); rec.BaseBranch != "dev" || !rec.Plan {
		t.Errorf("first turn %+v", rec)
	}
	expect(mustCall("forgerun_threads", `{}`), id+" · queued", "(from dev)", "Fix it")

	// Waiting returns once the turn is done: here, a plan.
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Claim(ctx)
		s.Finish(ctx, id, store.Outcome{State: task.Succeeded, BaseCommit: "base0", Reply: "1. change x"})
	}()
	expect(mustCall("forgerun_status", `{"thread":"`+id+`","wait":10}`), "waits for approval", "1. change x")

	expect(mustCall("forgerun_followup", `{"thread":"`+id+`","approve":true}`), "is queued")
	ts, _ := c.Thread(ctx, id)
	if ts[1].Prompt != approvePrompt {
		t.Errorf("approve sent %q", ts[1].Prompt)
	}
	expect(mustCall("forgerun_cancel", `{"thread":"`+id+`"}`), "is cancelled")

	expect(mustCall("forgerun_followup", `{"thread":"`+id+`","prompt":"Build it, and keep README as is"}`), "is queued")
	ts, _ = c.Thread(ctx, id)
	log := filepath.Join(t.TempDir(), "agent.log")
	os.WriteFile(log, []byte(strings.Repeat("x", 10000)+"\nthe end\n"), 0o600)
	s.Claim(ctx)
	s.Finish(ctx, ts[2].ID, store.Outcome{State: task.Succeeded, Diff: "+a\n", LogPath: log})
	if out := mustCall("forgerun_log", `{"thread":"`+id+`","bytes":20}`); out != "xxxxxxxxxxx\nthe end\n" {
		t.Errorf("log tail %q", out)
	}
	expect(mustCall("forgerun_diff", `{"thread":"`+id+`"}`), "+a")
	expect(mustCall("forgerun_push", `{"thread":"`+ts[2].ID+`","pr":true}`), "Pushed to forgerun/fix-it-abc. Pull request: https://github.com/o/r/pull/9")
	if len(pushed) != 1 || pushed[0].PRBase != "dev" || pushed[0].BaseCommit != "base0" || !pushed[0].OpenPR {
		t.Errorf("pushed %+v", pushed)
	}

	for name, args := range map[string]string{
		"forgerun_start":    `{"prompt":"p"}`,
		"forgerun_followup": `{"thread":"` + id + `"}`,
		"forgerun_branches": `{}`,
	} {
		if _, err := call(t, tools, name, args); err == nil {
			t.Errorf("%s %s: no error", name, args)
		}
	}
}
