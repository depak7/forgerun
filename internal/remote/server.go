package remote

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/depak7/forgerun/internal/api"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// Server is the orchestrator's side: it hands turns to workers, keeps
// track of which worker holds which session, and relays commands. For the
// API it stands in for the local worker pool and session registry
// (api.Pool and api.Sessions).
type Server struct {
	Store  store.Store
	Token  string // workers authenticate with it
	OutDir string // turns' logs are written here, as the local runner does
	// FetchToken returns the GitHub token a worker needs to fetch a
	// task's repo on its host ("" if the VM can clone it itself).
	FetchToken func(ctx context.Context, rec store.Record) (string, error)
	Log        *slog.Logger

	mu       sync.Mutex
	started  time.Time
	workers  map[string]*conn
	wake     chan struct{} // closed (and replaced) when there may be new work
	commands map[string][]Command
	waiting  map[string]chan CommandResult // command ID → whoever waits for its result
	lostSeen map[string]time.Time          // running task → first time no worker reported it
}

type conn struct {
	hb       Heartbeat
	lastSeen time.Time
}

var (
	_ api.Pool     = (*Server)(nil)
	_ api.Sessions = (*Server)(nil)
)

func (s *Server) init() {
	if s.workers == nil {
		s.started = time.Now()
		s.workers = map[string]*conn{}
		s.wake = make(chan struct{})
		s.commands = map[string][]Command{}
		s.waiting = map[string]chan CommandResult{}
		s.lostSeen = map[string]time.Time{}
	}
}

// ServeHTTP serves the worker endpoints under /api/worker/.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.init()
	s.mu.Unlock()
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
		http.Error(w, "bad worker token", http.StatusUnauthorized)
		return
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && p == pathHeartbeat:
		s.heartbeat(w, r)
	case r.Method == http.MethodPost && p == pathClaim:
		s.claim(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(p, pathThread):
		s.thread(w, r, strings.TrimPrefix(p, pathThread))
	case strings.HasPrefix(p, pathTask):
		id, what, _ := strings.Cut(strings.TrimPrefix(p, pathTask), "/")
		switch {
		case r.Method == http.MethodGet && what == "diff":
			s.diff(w, r, id)
		case r.Method == http.MethodPost && what == "log":
			s.appendLog(w, r, id)
		case r.Method == http.MethodPost && what == "finish":
			s.finish(w, r, id)
		default:
			http.NotFound(w, r)
		}
	case r.Method == http.MethodPost && strings.HasPrefix(p, pathResult):
		s.result(w, r, strings.TrimSuffix(strings.TrimPrefix(p, pathResult), "/result"))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	var hb Heartbeat
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&hb); err != nil || hb.Worker == "" {
		http.Error(w, "bad heartbeat", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	_, known := s.workers[hb.Worker]
	s.workers[hb.Worker] = &conn{hb: hb, lastSeen: time.Now()}
	cmds := s.commands[hb.Worker]
	delete(s.commands, hb.Worker)
	s.mu.Unlock()
	if !known {
		s.Log.Info("worker connected", "worker", hb.Worker, "agents", strings.Join(hb.Agents, ","), "sessions", len(hb.Sessions))
		s.Nudge() // it may be able to take waiting turns
	}
	writeJSON(w, HeartbeatReply{Commands: cmds})
}

// claim waits up to claimWait for a turn this worker may take: one of its
// own threads first; never a thread whose session is open on another
// live worker.
func (s *Server) claim(w http.ResponseWriter, r *http.Request) {
	var req ClaimRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.Worker == "" {
		http.Error(w, "bad claim", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	deadline := time.NewTimer(claimWait)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		prefer, avoid := s.affinity(req.Worker)
		wake := s.wake
		s.mu.Unlock()
		rec, ok, err := s.Store.ClaimFor(ctx, prefer, avoid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ok {
			a := Assignment{Task: rec}
			if s.FetchToken != nil {
				if a.FetchToken, err = s.FetchToken(ctx, rec); err != nil {
					s.fail(rec.ID, "getting the repo's access: "+err.Error())
					continue
				}
			}
			s.Log.Info("turn sent to worker", "id", rec.ID, "worker", req.Worker)
			writeJSON(w, a)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-wake:
		case <-time.After(5 * time.Second): // in case a nudge was missed
		}
	}
}

// affinity is which threads a worker should prefer (its own sessions) and
// avoid (sessions on other live workers). The caller holds s.mu.
func (s *Server) affinity(name string) (prefer, avoid []string) {
	for n, wk := range s.workers {
		if time.Since(wk.lastSeen) > workerLost {
			continue
		}
		for _, ss := range wk.hb.Sessions {
			if n == name {
				prefer = append(prefer, ss.Thread)
			} else {
				avoid = append(avoid, ss.Thread)
			}
		}
	}
	return prefer, avoid
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request, id string) {
	recs, err := s.Store.ThreadTasks(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, recs)
}

func (s *Server) diff(w http.ResponseWriter, r *http.Request, id string) {
	d, err := s.Store.Diff(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	io.WriteString(w, d)
}

// logPath is where a turn's log lives on the orchestrator, the same place
// the local runner writes it, so the API serves it the same way.
func (s *Server) logPath(id string) (string, error) {
	if strings.ContainsAny(id, `/\.`) {
		return "", errors.New("bad task id")
	}
	out, err := filepath.Abs(s.OutDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(out, id, "agent.log"), nil
}

func (s *Server) appendLog(w http.ResponseWriter, r *http.Request, id string) {
	path, err := s.logPath(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(r.Body, 8<<20)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) finish(w http.ResponseWriter, r *http.Request, id string) {
	var o store.Outcome
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&o); err != nil {
		http.Error(w, "bad outcome", http.StatusBadRequest)
		return
	}
	o.LogPath = "" // the worker's path means nothing here
	if path, err := s.logPath(id); err == nil {
		if _, err := os.Stat(path); err == nil {
			o.LogPath = path
		}
	}
	if err := s.Store.Finish(r.Context(), id, o); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.mu.Lock()
	delete(s.lostSeen, id)
	s.mu.Unlock()
	s.Log.Info("turn finished on worker", "id", id, "state", o.State)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) result(w http.ResponseWriter, r *http.Request, id string) {
	var res CommandResult
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&res); err != nil {
		http.Error(w, "bad result", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	ch := s.waiting[id]
	delete(s.waiting, id)
	s.mu.Unlock()
	if ch != nil {
		ch <- res
	}
	w.WriteHeader(http.StatusNoContent)
}

// Nudge wakes claims waiting for work (api.Pool).
func (s *Server) Nudge() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	close(s.wake)
	s.wake = make(chan struct{})
}

// Cancel asks the worker running a task to stop it (api.Pool). It reports
// false if no live worker runs it (then it may still be queued).
func (s *Server) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	for name, wk := range s.live() {
		if slices.Contains(wk.hb.Running, id) {
			s.commands[name] = append(s.commands[name], Command{ID: newID(), Kind: cmdCancel, Task: id})
			return true
		}
	}
	return false
}

// live is the workers heard from recently. The caller holds s.mu.
func (s *Server) live() map[string]*conn {
	out := map[string]*conn{}
	for n, wk := range s.workers {
		if time.Since(wk.lastSeen) <= workerLost {
			out[n] = wk
		}
	}
	return out
}

// owner finds the live worker holding a thread's session. The caller holds s.mu.
func (s *Server) owner(thread string) (string, task.OpenSession, bool) {
	for n, wk := range s.live() {
		for _, ss := range wk.hb.Sessions {
			if ss.Thread == thread {
				return n, ss, true
			}
		}
	}
	return "", task.OpenSession{}, false
}

// Session reports a thread's session, as its worker last described it.
func (s *Server) Session(thread string) task.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	_, ss, ok := s.owner(thread)
	if !ok {
		return task.SessionInfo{}
	}
	return task.SessionInfo{Open: true, Busy: ss.Busy, IdleUntil: ss.IdleUntil}
}

// OpenSessions counts sessions on live workers.
func (s *Server) OpenSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	n := 0
	for _, wk := range s.live() {
		n += len(wk.hb.Sessions)
	}
	return n
}

// CloseSession asks the thread's worker to end its session.
func (s *Server) CloseSession(thread string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := s.ask(ctx, thread, cmdEnd)
	switch {
	case err != nil:
		return err
	case res.NoOpen:
		return task.ErrNoSession
	case res.Busy:
		return task.ErrSessionBusy
	case res.Error != "":
		return errors.New(res.Error)
	}
	s.forget(thread) // closed now, not at the worker's next heartbeat
	return nil
}

// forget drops a thread's session from what workers last reported.
func (s *Server) forget(thread string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, wk := range s.workers {
		wk.hb.Sessions = slices.DeleteFunc(slices.Clone(wk.hb.Sessions), func(o task.OpenSession) bool { return o.Thread == thread })
	}
}

// Summarize asks the thread's worker for the agent's summary.
func (s *Server) Summarize(ctx context.Context, thread string) (string, error) {
	res, err := s.ask(ctx, thread, cmdSummarize)
	switch {
	case err != nil:
		return "", err
	case res.NoOpen:
		return "", task.ErrNoSession
	case res.Error != "":
		return "", errors.New(res.Error)
	}
	return res.Summary, nil
}

// ask sends a command to the thread's worker and waits for its result.
func (s *Server) ask(ctx context.Context, thread, kind string) (CommandResult, error) {
	s.mu.Lock()
	s.init()
	name, _, ok := s.owner(thread)
	if !ok {
		s.mu.Unlock()
		return CommandResult{}, task.ErrNoSession
	}
	cmd := Command{ID: newID(), Kind: kind, Thread: thread}
	ch := make(chan CommandResult, 1)
	s.waiting[cmd.ID] = ch
	s.commands[name] = append(s.commands[name], cmd)
	s.mu.Unlock()
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.waiting, cmd.ID)
		s.mu.Unlock()
		return CommandResult{}, fmt.Errorf("the worker didn't answer: %w", ctx.Err())
	}
}

// Workers describes the workers heard from recently, for the settings.
func (s *Server) Workers() []api.WorkerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	var out []api.WorkerInfo
	for n, wk := range s.live() {
		// Lists, never null: a worker without keys reports none.
		out = append(out, api.WorkerInfo{Name: n, LastSeen: wk.lastSeen, Agents: orEmpty(wk.hb.Agents), Adapters: orEmpty(wk.hb.Adapters),
			Sessions: len(wk.hb.Sessions), MaxSessions: wk.hb.MaxSessions, Running: len(wk.hb.Running)})
	}
	slices.SortFunc(out, func(a, b api.WorkerInfo) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Reap fails turns no live worker is running: their worker went away
// (crashed, lost its network) or was never heard from after a restart.
// A turn is given workerLost to show up in a heartbeat first. Run it every
// few seconds until ctx ends.
func (s *Server) Reap(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		running, err := s.Store.Running(ctx)
		if err != nil {
			continue
		}
		s.mu.Lock()
		s.init()
		reported := map[string]bool{}
		for _, wk := range s.live() {
			for _, id := range wk.hb.Running {
				reported[id] = true
			}
		}
		var lost []string
		for _, id := range running {
			if reported[id] {
				delete(s.lostSeen, id)
				continue
			}
			first, seen := s.lostSeen[id]
			if !seen {
				s.lostSeen[id] = time.Now()
				continue
			}
			if time.Since(first) > workerLost && time.Since(s.started) > workerLost {
				lost = append(lost, id)
				delete(s.lostSeen, id)
			}
		}
		s.mu.Unlock()
		for _, id := range lost {
			s.fail(id, "the worker running it went away")
		}
	}
}

func (s *Server) fail(id, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Store.Finish(ctx, id, store.Outcome{State: task.Failed, Error: reason}); err != nil {
		s.Log.Warn("failing a lost turn", "id", id, "err", err)
		return
	}
	s.Log.Warn("turn failed", "id", id, "reason", reason)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// orEmpty is ss, or an empty list for nil, so it encodes as [] not null.
func orEmpty(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}
