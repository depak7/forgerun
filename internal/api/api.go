// Package api serves forgerun's HTTP API, the backend for the web UI.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// Pool is what the API needs from the worker pool.
type Pool interface {
	Nudge()
	Cancel(id string) bool
}

type Server struct {
	store store.Store
	pool  Pool
	agent string // agent name recorded on new tasks
	log   *slog.Logger
}

// New returns the HTTP handler serving the API.
func New(s store.Store, p Pool, agent string, log *slog.Logger) http.Handler {
	srv := &Server{store: s, pool: p, agent: agent, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tasks", srv.createTask)
	mux.HandleFunc("GET /api/tasks", srv.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", srv.getTask)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", srv.cancelTask)
	mux.HandleFunc("GET /api/tasks/{id}/log", srv.taskLog)
	mux.HandleFunc("GET /api/tasks/{id}/patch", srv.taskPatch)
	mux.HandleFunc("GET /healthz", srv.health)
	return mux
}

// taskJSON is the API's view of a task. It is a promise to clients, kept
// separate from store.Record so storage changes never silently change it.
// Every field is always present so clients get one fixed shape.
type taskJSON struct {
	ID         string     `json:"id"`
	Repo       string     `json:"repo"`
	Prompt     string     `json:"prompt"`
	Agent      string     `json:"agent"`
	State      task.State `json:"state"`
	Error      string     `json:"error"`
	HasPatch   bool       `json:"has_patch"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`  // null until running
	FinishedAt *time.Time `json:"finished_at"` // null until finished
}

func toJSON(r store.Record) taskJSON {
	return taskJSON{
		ID: r.ID, Repo: r.Repo, Prompt: r.Prompt, Agent: r.AgentID, State: r.State, Error: r.Error,
		HasPatch: r.PatchPath != "", CreatedAt: r.CreatedAt.UTC(), StartedAt: utc(r.StartedAt), FinishedAt: utc(r.FinishedAt),
	}
}

// utc returns t in UTC, so clients always get "…Z" timestamps regardless of
// the server's time zone.
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo   string `json:"repo"`
		Prompt string `json:"prompt"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body larger than 1 MB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := validateRepo(req.Repo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	t := task.New(req.Repo, req.Prompt)
	if err := s.store.Create(r.Context(), t, s.agent); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.pool.Nudge()
	w.Header().Set("Location", "/api/tasks/"+t.ID)
	s.writeTask(w, r, t.ID, http.StatusCreated)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "limit must be a number from 1 to 100")
			return
		}
		limit = n
	}
	recs, err := s.store.List(r.Context(), limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]taskJSON, 0, len(recs)) // non-nil: an empty list encodes as [], not null
	for _, rec := range recs {
		out = append(out, toJSON(rec))
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	s.writeTask(w, r, r.PathValue("id"), http.StatusOK)
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.pool.Cancel(id) { // not running here: maybe still queued
		err := s.store.Cancel(r.Context(), id)
		if errors.Is(err, task.ErrInvalidTransition) {
			s.cancelConflict(w, r, id)
			return
		}
		if err != nil {
			s.storeError(w, r, err)
			return
		}
	}
	// 202: a running task stops asynchronously; its state changes shortly.
	s.writeTask(w, r, id, http.StatusAccepted)
}

// cancelConflict explains why a task can't be cancelled, in the user's terms.
func (s *Server) cancelConflict(w http.ResponseWriter, r *http.Request, id string) {
	rec, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if rec.State.Terminal() {
		writeError(w, http.StatusConflict, "task is already "+string(rec.State))
		return
	}
	// Claimed by a worker but not yet registered with the pool: a brief window.
	writeError(w, http.StatusConflict, "task is starting; retry the cancel")
}

func (s *Server) taskLog(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.getRecord(w, r)
	if !ok {
		return
	}
	if rec.LogPath == "" {
		writeError(w, http.StatusNotFound, "no log for this task yet")
		return
	}
	s.serveFile(w, r, rec.LogPath, "text/plain; charset=utf-8")
}

func (s *Server) taskPatch(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.getRecord(w, r)
	if !ok {
		return
	}
	if rec.PatchPath == "" {
		msg := "task made no changes"
		switch {
		case !rec.State.Terminal():
			msg = "task has not finished"
		case rec.State != task.Succeeded:
			msg = "task did not finish successfully"
		}
		writeError(w, http.StatusNotFound, msg)
		return
	}
	s.serveFile(w, r, rec.PatchPath, "text/x-diff; charset=utf-8")
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		s.log.Error("health check", "err", err)
		writeError(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "ok"})
}

// getRecord loads the task named in the path, writing an error response if
// that fails.
func (s *Server) getRecord(w http.ResponseWriter, r *http.Request) (store.Record, bool) {
	rec, err := s.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err)
		return store.Record{}, false
	}
	return rec, true
}

func (s *Server) writeTask(w http.ResponseWriter, r *http.Request, id string, status int) {
	rec, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeData(w, status, toJSON(rec))
}

// serveFile streams a task file. http.ServeContent handles Range requests,
// so clients can fetch only the new part of a growing log.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, path, contentType string) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "file no longer exists on the server")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

// storeError maps store errors to HTTP responses.
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "task not found")
	case errors.Is(err, task.ErrInvalidTransition):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.internalError(w, r, err)
	}
}

// internalError logs the details and tells the client nothing about them.
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// validateRepo allows only https URLs: git's other transports (file://,
// ext::) can read host files or run commands.
func validateRepo(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("repo must be an https:// git URL")
	}
	return nil
}
