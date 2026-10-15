package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
)

// turn adds a finished task to a thread (parent "" starts one).
func turn(t *testing.T, s *memory.Store, parent, prompt string, o store.Outcome) task.Task {
	t.Helper()
	ctx := context.Background()
	tk := task.New("https://github.com/o/r", prompt)
	tk.ParentID = parent
	if err := s.Create(ctx, tk, "cursor"); err != nil {
		t.Fatal(err)
	}
	s.Start(ctx, tk.ID)
	if err := s.Finish(ctx, tk.ID, o); err != nil {
		t.Fatal(err)
	}
	return tk
}

func pushed(t *testing.T, data json.RawMessage) []taskJSON {
	t.Helper()
	var out []taskJSON
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPushThread(t *testing.T) {
	var got []task.PublishRequest
	prs := fakePRs{open: true, got: &got, pub: task.Published{Branch: "forgerun/first-abc", PRURL: "https://github.com/o/r/pull/3", PRNumber: 3}}
	h, s, _ := setup(t, func(c *Config) {
		c.PRs, c.Sessions = prs, fakeSessions{summary: "## Done\nboth"}
	})
	first := turn(t, s, "", "first", store.Outcome{State: task.Succeeded, BaseCommit: "base0", Diff: "+a\n"})
	failed := turn(t, s, first.ID, "broken", store.Outcome{State: task.Failed, Diff: "+half\n"})
	second := turn(t, s, failed.ID, "second", store.Outcome{State: task.Succeeded, Diff: "+b\n"})

	// Every successful turn's changes, oldest first, as one push with a PR.
	steps := pushed(t, mustData(t, do(h, "POST", "/api/threads/"+first.ID+"/push", `{"pr":true}`), http.StatusOK))
	want := task.PublishRequest{ThreadID: first.ID, Repo: "https://github.com/o/r", BaseCommit: "base0",
		Patches: []string{"+a\n", "+b\n"}, Summary: "## Done\nboth", Requests: []string{"first", "broken", "second"}, OpenPR: true}
	if len(got) != 1 || !equalRequests(got[0], want) {
		t.Fatalf("published %+v\nwant %+v", got, want)
	}
	for _, st := range steps {
		if st.PR.Number != 3 || st.PR.Branch != "forgerun/first-abc" || st.Pushed != (st.ID != failed.ID) {
			t.Errorf("after the push, %s: pr=%+v pushed=%v", st.Prompt, st.PR, st.Pushed)
		}
	}
	if steps[2].Summary != "## Done\nboth" {
		t.Errorf("summary on the newest step: %q", steps[2].Summary)
	}

	// Nothing new: refused. A new turn: only its changes, onto the branch and PR.
	decode(t, do(h, "POST", "/api/threads/"+first.ID+"/push", `{}`), http.StatusConflict)
	third := turn(t, s, second.ID, "third", store.Outcome{State: task.Succeeded, Diff: "+c\n"})
	mustData(t, do(h, "POST", "/api/threads/"+first.ID+"/push", `{}`), http.StatusOK)
	if len(got) != 2 || len(got[1].Patches) != 1 || got[1].Patches[0] != "+c\n" || got[1].Branch != "forgerun/first-abc" || got[1].PRNumber != 3 {
		t.Errorf("second push: %+v", got[1])
	}
	if rec, _ := s.Get(context.Background(), third.ID); !rec.Pushed {
		t.Error("third turn not marked pushed")
	}

	// Only a thread's first task names it.
	decode(t, do(h, "POST", "/api/threads/"+second.ID+"/push", `{}`), http.StatusNotFound)
}

func TestPushFailure(t *testing.T) {
	prs := fakePRs{pubErr: errors.New("git push: rejected")}
	h, s, _ := setup(t, func(c *Config) { c.PRs = prs })
	first := turn(t, s, "", "first", store.Outcome{State: task.Succeeded, Diff: "+a\n"})
	_, apiErr := decode(t, do(h, "POST", "/api/threads/"+first.ID+"/push", `{}`), http.StatusBadGateway)
	if !strings.Contains(apiErr.Message, "rejected") {
		t.Errorf("error = %q", apiErr.Message)
	}
	rec, _ := s.Get(context.Background(), first.ID)
	if rec.Pushed || rec.PR.Error != "git push: rejected" {
		t.Errorf("after a failed push: pushed=%v pr=%+v", rec.Pushed, rec.PR)
	}

	// A step still running: refused.
	running := task.New("https://github.com/o/r", "busy")
	s.Create(context.Background(), running, "cursor")
	s.Start(context.Background(), running.ID)
	decode(t, do(h, "POST", "/api/threads/"+running.ID+"/push", `{}`), http.StatusConflict)
}

func TestSessionEndpoints(t *testing.T) {
	idle := time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC)
	var closed []string
	h, s, _ := setup(t, func(c *Config) {
		c.Sessions = fakeSessions{info: task.SessionInfo{Open: true, IdleUntil: idle}, closed: &closed}
	})
	first := turn(t, s, "", "first", store.Outcome{State: task.Succeeded})

	var sess sessionJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/threads/"+first.ID+"/session", ""), http.StatusOK), &sess)
	if !sess.Open || sess.IdleUntil == nil || !sess.IdleUntil.Equal(idle) {
		t.Errorf("session = %+v", sess)
	}
	mustData(t, do(h, "POST", "/api/threads/"+first.ID+"/end", ""), http.StatusOK)
	if len(closed) != 1 || closed[0] != first.ID {
		t.Errorf("closed %v", closed)
	}
	decode(t, do(h, "GET", "/api/threads/t-nope/session", ""), http.StatusNotFound)

	for _, err := range []error{task.ErrNoSession, task.ErrSessionBusy} {
		h, s, _ := setup(t, func(c *Config) { c.Sessions = fakeSessions{closeErr: err} })
		tk := turn(t, s, "", "x", store.Outcome{State: task.Succeeded})
		decode(t, do(h, "POST", "/api/threads/"+tk.ID+"/end", ""), http.StatusConflict)
	}
}

func equalRequests(a, b task.PublishRequest) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
