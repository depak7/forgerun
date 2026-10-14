// Package api serves forgerun's HTTP API, the backend for the web UI.
package api

import (
	"cmp"
	"context"
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

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// Pool is what the API needs from the worker pool.
type Pool interface {
	Nudge()
	Cancel(id string) bool
}

// Config is everything the API needs.
type Config struct {
	Store        store.Store
	Pool         Pool
	Agent        string // id of the agent new tasks run on, e.g. "cursor"; only its models are offered
	DefaultModel string // model family used when a request names none; "" = the agent's own default
	RequireZDR   bool   // offer only models with zero data retention
	PullRequests bool   // tasks may ask for a pull request (the server has a GitHub token)
	Log          *slog.Logger
}

type Server struct {
	store        store.Store
	pool         Pool
	agent        string
	defaultModel string
	requireZDR   bool
	pullRequests bool
	log          *slog.Logger
}

// New returns the HTTP handler serving the API.
func New(cfg Config) http.Handler {
	srv := &Server{store: cfg.Store, pool: cfg.Pool, agent: cfg.Agent,
		defaultModel: cfg.DefaultModel, requireZDR: cfg.RequireZDR, pullRequests: cfg.PullRequests, log: cfg.Log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tasks", srv.createTask)
	mux.HandleFunc("GET /api/tasks", srv.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", srv.getTask)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", srv.cancelTask)
	mux.HandleFunc("GET /api/tasks/{id}/log", srv.taskLog)
	mux.HandleFunc("GET /api/tasks/{id}/patch", srv.taskPatch)
	mux.HandleFunc("GET /api/agents", srv.listAgents)
	mux.HandleFunc("GET /api/models", srv.listModels)
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
	Model      string     `json:"model"`  // family, e.g. "claude-opus-5-5" or "auto"
	Effort     string     `json:"effort"` // "" for plain variants
	Thinking   bool       `json:"thinking"`
	Fast       bool       `json:"fast"`
	ModelID    string     `json:"model_id"` // exact id passed to the agent
	State      task.State `json:"state"`
	Error      string     `json:"error"`
	HasPatch   bool       `json:"has_patch"`
	SnapshotID *int64     `json:"snapshot_id"` // snapshot it was restored from; null = cold start
	PR         prJSON     `json:"pr"`
	Summary    string     `json:"summary"` // the agent's summary of the conversation (PR tasks); "" if none
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`  // null until running
	FinishedAt *time.Time `json:"finished_at"` // null until finished
}

func toJSON(r store.Record) taskJSON {
	return taskJSON{
		ID: r.ID, Repo: r.Repo, Prompt: r.Prompt, Agent: r.AgentID,
		Model: r.Model.Family, Effort: r.Model.Effort, Thinking: r.Model.Thinking, Fast: r.Model.Fast, ModelID: r.Model.ID,
		State: r.State, Error: r.Error, HasPatch: r.PatchPath != "", SnapshotID: r.SnapshotID,
		PR: prJSON{Requested: r.PR.Requested, URL: r.PR.URL, Error: r.PR.Error}, Summary: r.Summary,
		CreatedAt: r.CreatedAt.UTC(), StartedAt: utc(r.StartedAt), FinishedAt: utc(r.FinishedAt),
	}
}

// prJSON is a task's pull request in the API.
type prJSON struct {
	Requested bool   `json:"requested"`
	URL       string `json:"url"`   // "" until opened
	Error     string `json:"error"` // why it wasn't opened; "" otherwise
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
		Repo     string `json:"repo"`
		Prompt   string `json:"prompt"`
		Model    string `json:"model"`
		Effort   string `json:"effort"`
		Thinking *bool  `json:"thinking"` // nil = not sent
		Fast     *bool  `json:"fast"`
		PR       bool   `json:"pr"` // open a pull request with the changes
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
	if req.PR {
		// Refuse now rather than fail after the agent has done the work.
		if !s.pullRequests {
			writeError(w, http.StatusBadRequest, "pull requests are disabled on this server (it has no GITHUB_TOKEN)")
			return
		}
		if _, err := forge.ParseRepo(req.Repo); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	cat, err := s.catalog(r.Context(), s.agent)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	v, err := cat.Resolve(models.Choice{Model: req.Model, Effort: req.Effort, Thinking: req.Thinking, Fast: req.Fast}, s.defaultModel)
	if errors.Is(err, models.ErrInvalidChoice) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	t := task.New(req.Repo, req.Prompt)
	t.ModelID, t.PR = v.ID, req.PR
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

// catalog loads agentID's enabled models, minus those without zero data
// retention if required. It is read from the store on every call, so a
// newly inserted model is usable at once.
func (s *Server) catalog(ctx context.Context, agentID string) (*models.Catalog, error) {
	vs, err := s.models(ctx)
	if err != nil {
		return nil, err
	}
	return models.NewCatalog(agentID, vs), nil
}

// models returns every enabled model, minus non-ZDR ones if required.
func (s *Server) models(ctx context.Context) ([]models.Variant, error) {
	vs, err := s.store.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	if s.requireZDR {
		vs = models.OnlyZDR(vs)
	}
	return vs, nil
}

// defaultFamily is the model family used when a request names none. The
// server's DefaultModel applies only to the agent this server runs.
func (s *Server) defaultFamily(agentID string, cat *models.Catalog) string {
	if agentID == s.agent && s.defaultModel != "" {
		return s.defaultModel
	}
	return cat.DefaultFamily()
}

type agentJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DefaultModel string `json:"default_model"` // model family
	Runnable     bool   `json:"runnable"`      // this server can run tasks with it
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	vs, err := s.models(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]agentJSON, 0, len(agents))
	for _, a := range agents {
		cat := models.NewCatalog(a.ID, vs)
		out = append(out, agentJSON{ID: a.ID, Name: a.Name, DefaultModel: s.defaultFamily(a.ID, cat), Runnable: a.ID == s.agent})
	}
	writeData(w, http.StatusOK, out)
}

type familyJSON struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Efforts       []string `json:"efforts"`        // [] when the family has none
	DefaultEffort string   `json:"default_effort"` // "" = the plain variant
	Thinking      bool     `json:"thinking"`
	Fast          bool     `json:"fast"`
	ZDR           bool     `json:"zdr"`
}

// listModels serves GET /api/models?agent=<id>; without ?agent= it lists
// the models of the agent this server runs.
func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	agentID := cmp.Or(r.URL.Query().Get("agent"), s.agent)
	a, err := s.store.GetAgent(r.Context(), agentID)
	if errors.Is(err, store.ErrAgentNotFound) || (err == nil && !a.Enabled) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cat, err := s.catalog(r.Context(), agentID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	fams := cat.Families()
	out := make([]familyJSON, 0, len(fams))
	for _, f := range fams {
		efforts := f.Efforts
		if efforts == nil {
			efforts = []string{} // encode as [], never null
		}
		out = append(out, familyJSON{ID: f.ID, Name: f.Name, Efforts: efforts, DefaultEffort: f.DefaultEffort,
			Thinking: f.Thinking, Fast: f.Fast, ZDR: f.ZDR})
	}
	writeData(w, http.StatusOK, struct {
		Agent    string       `json:"agent"`
		Runnable bool         `json:"runnable"` // this server can run tasks with this agent
		Default  string       `json:"default"`
		Models   []familyJSON `json:"models"`
	}{agentID, agentID == s.agent, s.defaultFamily(agentID, cat), out})
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
