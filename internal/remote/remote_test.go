package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
)

const token = "worker-token-0123456789"

func setup(t *testing.T) (*Server, *memory.Store, string) {
	t.Helper()
	s := memory.New(models.Variant{ID: "auto", AgentID: "fake", Family: "auto", AgentDefault: true})
	rs := &Server{Store: s, Token: token, OutDir: t.TempDir(), Log: slog.New(slog.DiscardHandler)}
	srv := httptest.NewServer(rs)
	t.Cleanup(srv.Close)
	return rs, s, srv.URL
}

func client(url, name string) *Client {
	return &Client{URL: url, Token: token, Name: name, Log: slog.New(slog.DiscardHandler)}
}

func queue(t *testing.T, s *memory.Store, prompt, parent string) task.Task {
	t.Helper()
	tk := task.New("https://github.com/o/r", prompt)
	tk.ParentID = parent
	if err := s.Create(context.Background(), tk, "fake"); err != nil {
		t.Fatal(err)
	}
	return tk
}

func beat(t *testing.T, url string, hb Heartbeat) HeartbeatReply {
	t.Helper()
	var reply HeartbeatReply
	if _, err := client(url, hb.Worker).postJSON(context.Background(), pathHeartbeat, hb, &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

// A turn goes to the worker over HTTP; its log and outcome come back.
func TestClaimLogFinish(t *testing.T) {
	rs, s, url := setup(t)
	rs.FetchToken = func(_ context.Context, rec store.Record) (string, error) { return "gho_for_" + rec.Prompt, nil }
	c := client(url, "mac")
	tk := queue(t, s, "private", "")
	ctx := context.Background()

	rec, ok, err := c.Claim(ctx)
	if err != nil || !ok || rec.ID != tk.ID || rec.State != task.Running {
		t.Fatalf("Claim = %+v, %v, %v", rec, ok, err)
	}
	if f, _ := c.Fetchers(ctx, rec); f == nil {
		t.Error("a private repo's turn needs a fetcher")
	}
	sink := c.LogSink(tk.ID)
	io.WriteString(sink, "==> cloning\n")
	io.WriteString(sink, "done\n")
	sink.Close()
	if err := c.Finish(ctx, tk.ID, store.Outcome{State: task.Succeeded, Diff: "+x\n", Reply: "ok", LogPath: "/worker/path"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, tk.ID)
	if got.State != task.Succeeded || !got.HasDiff || got.Reply != "ok" {
		t.Errorf("after Finish: %+v", got)
	}
	if b, _ := os.ReadFile(got.LogPath); string(b) != "==> cloning\ndone\n" || !strings.HasPrefix(got.LogPath, rs.OutDir) {
		t.Errorf("log %s = %q", got.LogPath, b)
	}
	if d, _ := c.Diff(ctx, tk.ID); d != "+x\n" {
		t.Errorf("Diff = %q", d)
	}
	if recs, _ := c.ThreadTasks(ctx, tk.ID); len(recs) != 1 {
		t.Errorf("ThreadTasks = %d", len(recs))
	}
	// Nothing queued: the claim waits, then comes back empty.
	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, ok, _ := c.Claim(cctx); ok {
		t.Error("claimed with nothing queued")
	}
}

// A thread's next turn goes to the worker holding its session.
func TestSessionAffinity(t *testing.T) {
	_, s, url := setup(t)
	ctx := context.Background()
	first := queue(t, s, "first", "")
	s.Start(ctx, first.ID)
	s.Finish(ctx, first.ID, store.Outcome{State: task.Succeeded})
	beat(t, url, Heartbeat{Worker: "a", Sessions: []task.OpenSession{{Thread: first.ID, Agent: "fake"}}})
	beat(t, url, Heartbeat{Worker: "b"})
	next := queue(t, s, "next", first.ID)

	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if rec, ok, _ := client(url, "b").Claim(cctx); ok {
		t.Fatalf("worker b took %s, whose session is on a", rec.ID)
	}
	if rec, ok, _ := client(url, "a").Claim(ctx); !ok || rec.ID != next.ID {
		t.Errorf("worker a got %+v, %v", rec, ok)
	}
}

// Commands ride on heartbeats; summarize and end wait for the worker's answer.
func TestCommands(t *testing.T) {
	rs, s, url := setup(t)
	ctx := context.Background()
	tk := queue(t, s, "x", "")
	s.Start(ctx, tk.ID)
	beat(t, url, Heartbeat{Worker: "mac", Running: []string{tk.ID}, Sessions: []task.OpenSession{{Thread: tk.ID, Agent: "fake"}}})
	if !rs.Cancel(tk.ID) || rs.Cancel("t-other") {
		t.Error("Cancel should find the running turn, and only it")
	}
	if info := rs.Session(tk.ID); !info.Open || rs.OpenSessions() != 1 {
		t.Errorf("session = %+v", info)
	}

	done := make(chan string, 1)
	go func() {
		sum, err := rs.Summarize(ctx, tk.ID)
		if err != nil {
			sum = "error: " + err.Error()
		}
		done <- sum
	}()
	var cmds []Command
	for range 50 {
		cmds = append(cmds, beat(t, url, Heartbeat{Worker: "mac", Running: []string{tk.ID}, Sessions: []task.OpenSession{{Thread: tk.ID}}}).Commands...)
		if len(cmds) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var summarize *Command
	for i, c := range cmds {
		if c.Kind == cmdSummarize {
			summarize = &cmds[i]
		}
	}
	if len(cmds) != 2 || cmds[0].Kind != cmdCancel || cmds[0].Task != tk.ID || summarize == nil {
		t.Fatalf("commands = %+v", cmds)
	}
	b, _ := json.Marshal(CommandResult{Summary: "## Done"})
	resp, err := client(url, "mac").do(ctx, http.MethodPost, pathResult+summarize.ID+"/result", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := <-done; got != "## Done" {
		t.Errorf("Summarize = %q", got)
	}
	// Ending a session: the worker answers, and the session is gone at once.
	ended := make(chan error, 1)
	go func() { ended <- rs.CloseSession(tk.ID) }()
	var end *Command
	for range 50 {
		for _, c := range beat(t, url, Heartbeat{Worker: "mac", Running: []string{tk.ID}, Sessions: []task.OpenSession{{Thread: tk.ID}}}).Commands {
			if c.Kind == cmdEnd {
				end = &c
			}
		}
		if end != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if end == nil {
		t.Fatal("no end command")
	}
	b, _ = json.Marshal(CommandResult{})
	resp, err = client(url, "mac").do(ctx, http.MethodPost, pathResult+end.ID+"/result", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := <-ended; err != nil || rs.Session(tk.ID).Open {
		t.Errorf("after ending: %v, open %v", err, rs.Session(tk.ID).Open)
	}
	if err := rs.CloseSession("t-none"); err != task.ErrNoSession {
		t.Errorf("CloseSession of no session: %v", err)
	}
}

// A running turn no worker reports fails once it's been missing long enough.
func TestReapLostTurns(t *testing.T) {
	old := workerLost
	workerLost = 50 * time.Millisecond
	t.Cleanup(func() { workerLost = old })
	rs, s, _ := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tk := queue(t, s, "x", "")
	s.Start(ctx, tk.ID)
	go rs.Reap(ctx)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if rec, _ := s.Get(ctx, tk.ID); rec.State == task.Failed {
			if !strings.Contains(rec.Error, "went away") {
				t.Errorf("error = %q", rec.Error)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the lost turn was never failed")
}

func TestWorkerTokenRequired(t *testing.T) {
	_, _, url := setup(t)
	bad := &Client{URL: url, Token: "wrong", Name: "x", Log: slog.New(slog.DiscardHandler)}
	if _, _, err := bad.Claim(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad token: %v", err)
	}
}

func TestLogPathRefusesTraversal(t *testing.T) {
	rs, _, _ := setup(t)
	for _, id := range []string{"../etc", "a/b", "x.y"} {
		if _, err := rs.logPath(id); err == nil {
			t.Errorf("logPath(%q) should be refused", id)
		}
	}
	if p, err := rs.logPath("t-1"); err != nil || filepath.Base(filepath.Dir(p)) != "t-1" {
		t.Errorf("logPath(t-1) = %s, %v", p, err)
	}
}
