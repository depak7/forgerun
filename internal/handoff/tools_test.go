package handoff

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/api"
	"github.com/depak7/forgerun/internal/mcp"
	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
)

type nopPool struct{}

func (nopPool) Nudge()                {}
func (nopPool) Cancel(id string) bool { return false }

// server is a forgerun API in local mode on a memory store.
func server(t *testing.T) (*Client, *memory.Store) {
	t.Helper()
	s := memory.New(models.Variant{ID: "auto", AgentID: "cursor", Family: "auto", Name: "Auto", IsDefault: true, AgentDefault: true, ZDR: true})
	h := api.New(api.Config{Store: s, Pool: nopPool{}, Agent: "cursor", Log: slog.New(slog.DiscardHandler),
		Agents: agent.NewSet(agent.StaticCredentials{"CURSOR_API_KEY": "k"}, &agent.Cursor{})})
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
