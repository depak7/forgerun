package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// pushTimeout bounds pushing a thread, summary included.
const pushTimeout = 4 * time.Minute

// sessionJSON is a thread's session: whether its VM is open.
type sessionJSON struct {
	Open      bool       `json:"open"`
	Busy      bool       `json:"busy"`       // a turn or a summary is running in it
	IdleUntil *time.Time `json:"idle_until"` // when it closes if left idle; null if busy or never
}

func (s *Server) sessionOf(thread string) sessionJSON {
	if s.sessions == nil {
		return sessionJSON{}
	}
	info := s.sessions.Session(thread)
	out := sessionJSON{Open: info.Open, Busy: info.Busy}
	if !info.IdleUntil.IsZero() {
		t := info.IdleUntil.UTC()
		out.IdleUntil = &t
	}
	return out
}

// threadTasks loads a thread by its ID (its first task's), writing a 404
// if there's no such thread.
func (s *Server) threadTasks(w http.ResponseWriter, r *http.Request) ([]store.Record, bool) {
	id := r.PathValue("id")
	thread, err := s.store.ThreadTasks(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return nil, false
	}
	if len(thread) == 0 || thread[0].ID != id || !viewerOf(r).canSee(thread[0]) {
		writeError(w, http.StatusNotFound, "no thread "+id)
		return nil, false
	}
	return thread, true
}

// getSession serves GET /api/threads/{id}/session.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.threadTasks(w, r); !ok {
		return
	}
	writeData(w, http.StatusOK, s.sessionOf(r.PathValue("id")))
}

// endSession serves POST /api/threads/{id}/end: close the thread's session
// now. The work in progress is kept; the next message reopens it.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.threadTasks(w, r); !ok {
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusConflict, "this thread has no open session")
		return
	}
	switch err := s.sessions.CloseSession(r.PathValue("id")); {
	case errors.Is(err, task.ErrNoSession):
		writeError(w, http.StatusConflict, "this thread has no open session")
	case errors.Is(err, task.ErrSessionBusy):
		writeError(w, http.StatusConflict, "the session is busy; end it when the current step finishes")
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeData(w, http.StatusOK, s.sessionOf(r.PathValue("id")))
	}
}

// pushThread serves POST /api/threads/{id}/push {"pr": bool}: push every
// unpushed change in the thread to its branch as one commit, and open its
// pull request if asked (or refresh the one it has). It answers with the
// thread's tasks.
func (s *Server) pushThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PR bool `json:"pr"` // open a pull request if the thread has none
	}
	if !decodeBody(w, r, &req) {
		return
	}
	thread, ok := s.threadTasks(w, r)
	if !ok {
		return
	}
	id := thread[0].ID
	if s.prs == nil {
		writeError(w, http.StatusBadRequest, "this server can't push (it has no GITHUB_TOKEN)")
		return
	}
	latest := thread[len(thread)-1]
	if !latest.State.Terminal() {
		writeError(w, http.StatusConflict, "push when the current step finishes")
		return
	}
	if _, busy := s.pushing.LoadOrStore(id, struct{}{}); busy {
		writeError(w, http.StatusConflict, "this thread is already being pushed")
		return
	}
	defer s.pushing.Delete(id)

	var ids, patches, requests []string
	for _, t := range thread {
		requests = append(requests, t.Prompt)
		if t.State != task.Succeeded || !t.HasDiff || t.Pushed {
			continue
		}
		diff, err := s.store.Diff(r.Context(), t.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		ids, patches = append(ids, t.ID), append(patches, diff)
	}
	pr := latest.PR
	openOnly := req.PR && pr.Number == 0 && pr.Branch != "" // the branch is pushed; only the pull request is new
	if len(patches) == 0 && !openOnly {
		writeError(w, http.StatusConflict, "nothing to push: every change in this thread is already on its branch")
		return
	}

	// The push goes on even if the client goes away: it's the user's work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), pushTimeout)
	defer cancel()
	wantPR := req.PR || pr.Number > 0
	var summary string
	if wantPR && s.sessions != nil {
		// The agent's own summary of the conversation becomes the description.
		var err error
		if summary, err = s.sessions.Summarize(ctx, id); err != nil && !errors.Is(err, task.ErrNoSession) {
			s.log.Warn("no summary for the pull request", "thread", id, "err", err)
		}
	}
	tok, err := s.tokenOf(ctx, thread[0].Owner)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	pub, err := s.prs.Publish(ctx, task.PublishRequest{Token: tok,
		ThreadID: id, Repo: thread[0].Repo, BaseCommit: thread[0].BaseCommit, Patches: patches,
		Summary: summary, Requests: requests, OpenPR: req.PR, Branch: pr.Branch, PRNumber: pr.Number,
	})
	push := store.Push{LatestID: latest.ID, Summary: summary, PR: store.PullRequest{
		Requested: wantPR || pr.Requested,
		URL:       cmp.Or(pub.PRURL, pr.URL), Number: cmp.Or(pub.PRNumber, pr.Number), Branch: cmp.Or(pub.Branch, pr.Branch),
	}}
	if pub.Branch != "" { // the commit reached the branch, even if the pull request then failed
		push.Pushed = ids
	}
	if err != nil {
		push.PR.Error = err.Error()
	}
	if rerr := s.store.RecordPush(ctx, id, push); rerr != nil {
		s.internalError(w, r, rerr)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	updated, err := s.store.ThreadTasks(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]taskJSON, len(updated))
	for i, t := range updated {
		out[i] = toJSON(t)
	}
	writeData(w, http.StatusOK, out)
}
