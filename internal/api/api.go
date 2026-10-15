// Package api serves forgerun's HTTP API, the backend for the web UI.
package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/depak7/forgerun/internal/agent"
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
	Agents       agent.Set         // the agents tasks can run on, and their credentials
	Agent        string            // the agent for requests that name none, e.g. "cursor"
	DefaultModel string            // model family used when a request names none; "" = the agent's own default
	RequireZDR   bool              // offer only models with zero data retention
	PRs          PRs               // pull requests; nil = off (the server has no GitHub token)
	OutDir       string            // where the runner writes task results, to serve logs while tasks run
	GitHubUser   string            // the account pull requests are opened as; "" if PRs are off
	Snapshots    *SnapshotSettings // nil = snapshots are off
	Log          *slog.Logger
}

// PRs is what the API needs to know about pull requests: whether one is
// still open, so follow-ups can be refused before any work is done.
// publish.Publisher implements it.
type PRs interface {
	IsOpen(ctx context.Context, repo string, number int) (bool, error)
}

type Server struct {
	agents       agent.Set
	store        store.Store
	pool         Pool
	agent        string
	defaultModel string
	requireZDR   bool
	prs          PRs
	outDir       string
	githubUser   string
	snapshots    *SnapshotSettings
	log          *slog.Logger
}

// New returns the HTTP handler serving the API.
func New(cfg Config) http.Handler {
	srv := &Server{agents: cfg.Agents, store: cfg.Store, pool: cfg.Pool, agent: cfg.Agent,
		defaultModel: cfg.DefaultModel, requireZDR: cfg.RequireZDR, prs: cfg.PRs, outDir: cfg.OutDir, githubUser: cfg.GitHubUser, snapshots: cfg.Snapshots, log: cfg.Log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tasks", srv.createTask)
	mux.HandleFunc("GET /api/tasks", srv.listTasks)
	mux.HandleFunc("GET /api/tasks/{id}", srv.getTask)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", srv.cancelTask)
	mux.HandleFunc("POST /api/tasks/{id}/followups", srv.createFollowUp)
	mux.HandleFunc("GET /api/tasks/{id}/thread", srv.getThread)
	mux.HandleFunc("GET /api/tasks/{id}/log", srv.taskLog)
	mux.HandleFunc("GET /api/tasks/{id}/patch", srv.taskPatch)
	mux.HandleFunc("GET /api/agents", srv.listAgents)
	mux.HandleFunc("GET /api/threads", srv.listThreads)
	mux.HandleFunc("GET /api/status", srv.status)
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
	ParentID   *string    `json:"parent_id"`   // the task this one follows up; null for a thread's first task
	ThreadID   string     `json:"thread_id"`   // the thread's first task
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
		PR:       prJSON{Requested: r.PR.Requested, URL: r.PR.URL, Number: r.PR.Number, Branch: r.PR.Branch, Error: r.PR.Error},
		Summary:  r.Summary,
		ParentID: optional(r.ParentID), ThreadID: r.ThreadID,
		CreatedAt: r.CreatedAt.UTC(), StartedAt: utc(r.StartedAt), FinishedAt: utc(r.FinishedAt),
	}
}

// prJSON is a task's pull request in the API.
type prJSON struct {
	Requested bool   `json:"requested"`
	URL       string `json:"url"`    // "" until opened
	Number    int    `json:"number"` // 0 until opened
	Branch    string `json:"branch"` // "" until opened
	Error     string `json:"error"`  // why it wasn't opened; "" otherwise
}

// optional maps "" to a JSON null.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
		PR       bool   `json:"pr"`    // open a pull request with the changes
		Agent    string `json:"agent"` // "" = the server's default agent
	}
	if !decodeBody(w, r, &req) {
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
		if s.prs == nil {
			writeError(w, http.StatusBadRequest, "pull requests are disabled on this server (it has no GITHUB_TOKEN)")
			return
		}
		if _, err := forge.ParseRepo(req.Repo); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	agentID := cmp.Or(req.Agent, s.agent)
	if !s.checkAgent(w, r, agentID) {
		return
	}
	v, ok := s.resolveModel(w, r, agentID, models.Choice{Model: req.Model, Effort: req.Effort, Thinking: req.Thinking, Fast: req.Fast})
	if !ok {
		return
	}

	t := task.New(req.Repo, req.Prompt)
	t.ModelID, t.PR = v.ID, req.PR
	if err := s.store.Create(r.Context(), t, agentID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.pool.Nudge()
	w.Header().Set("Location", "/api/tasks/"+t.ID)
	s.writeTask(w, r, t.ID, http.StatusCreated)
}

// decodeBody reads a JSON request body of at most 1 MB into v, rejecting
// unknown fields. It writes the error response itself if it can't.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body larger than 1 MB")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// limitParam reads ?limit= (1 to 100, default 20), writing the error
// response itself if it's invalid.
func limitParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 20, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 100 {
		writeError(w, http.StatusBadRequest, "limit must be a number from 1 to 100")
		return 0, false
	}
	return n, true
}

// resolveModel turns a request's model choice into an exact model of the
// server's agent, writing the error response itself if it can't.
func (s *Server) resolveModel(w http.ResponseWriter, r *http.Request, agentID string, ch models.Choice) (models.Variant, bool) {
	cat, err := s.catalog(r.Context(), agentID)
	if err != nil {
		s.internalError(w, r, err)
		return models.Variant{}, false
	}
	v, err := cat.Resolve(ch, s.defaultFamily(agentID, cat))
	if errors.Is(err, models.ErrInvalidChoice) {
		writeError(w, http.StatusBadRequest, err.Error())
		return models.Variant{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return models.Variant{}, false
	}
	return v, true
}

// checkAgent makes sure a task can run on agentID now: forgerun has an
// adapter for it, it's enabled, and its credential is configured. It writes
// the error response itself if not.
func (s *Server) checkAgent(w http.ResponseWriter, r *http.Request, agentID string) bool {
	if _, err := s.agents.Get(agentID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	a, err := s.store.GetAgent(r.Context(), agentID)
	if errors.Is(err, store.ErrAgentNotFound) || (err == nil && !a.Enabled) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("agent %q is not enabled", agentID))
		return false
	}
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	// Checked now so the user hears at once, not after the task waits in the queue.
	if err := s.agents.Check(r.Context(), agentID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// runnable reports whether forgerun has an adapter for the agent.
func (s *Server) runnable(agentID string) bool {
	_, err := s.agents.Get(agentID)
	return err == nil
}

// createFollowUp continues a thread's pull request with a new request. The
// agent works on the pull request's branch with the thread's summary as
// context, and the result is pushed to the same pull request.
func (s *Server) createFollowUp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt   string `json:"prompt"`
		Agent    string `json:"agent"` // "" = the agent of the task it follows
		Model    string `json:"model"`
		Effort   string `json:"effort"`
		Thinking *bool  `json:"thinking"`
		Fast     *bool  `json:"fast"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	if s.prs == nil {
		writeError(w, http.StatusBadRequest, "pull requests are disabled on this server (it has no GITHUB_TOKEN)")
		return
	}
	parent, ok := s.getRecord(w, r)
	if !ok {
		return
	}
	// Explain the thread rules in the user's terms; the store enforces them too.
	if parent.PR.Number == 0 {
		writeError(w, http.StatusConflict, "task "+parent.ID+" has no pull request to follow up on")
		return
	}
	if !parent.State.Terminal() {
		writeError(w, http.StatusConflict, "task "+parent.ID+" is still "+string(parent.State)+"; follow up when it finishes")
		return
	}
	thread, err := s.store.ThreadTasks(r.Context(), parent.ThreadID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if latest := thread[len(thread)-1]; latest.ID != parent.ID {
		writeError(w, http.StatusConflict, "follow up on the latest task in this thread: "+latest.ID)
		return
	}
	open, err := s.prs.IsOpen(r.Context(), parent.Repo, parent.PR.Number)
	if err != nil {
		writeError(w, http.StatusBadGateway, "checking the pull request on GitHub: "+err.Error())
		return
	}
	if !open {
		writeError(w, http.StatusConflict, fmt.Sprintf("pull request #%d is closed or merged", parent.PR.Number))
		return
	}

	// A thread can switch agents: the context it carries is agent-neutral.
	agentID := cmp.Or(req.Agent, parent.AgentID)
	if !s.checkAgent(w, r, agentID) {
		return
	}
	v, ok := s.resolveModel(w, r, agentID, models.Choice{Model: req.Model, Effort: req.Effort, Thinking: req.Thinking, Fast: req.Fast})
	if !ok {
		return
	}
	t := task.New(parent.Repo, req.Prompt)
	t.ModelID, t.PR, t.ParentID = v.ID, true, parent.ID
	if err := s.store.Create(r.Context(), t, agentID); err != nil {
		s.storeError(w, r, err)
		return
	}
	s.pool.Nudge()
	w.Header().Set("Location", "/api/tasks/"+t.ID)
	s.writeTask(w, r, t.ID, http.StatusCreated)
}

// threadJSON is a thread in the list: its first request, and the state of
// its newest step.
type threadJSON struct {
	ID             string    `json:"id"`    // the first task's ID
	Title          string    `json:"title"` // the first task's prompt
	Repo           string    `json:"repo"`
	Steps          int       `json:"steps"`
	Agents         []string  `json:"agents"` // each step's agent, oldest first
	Latest         taskJSON  `json:"latest"`
	NeedsAttention bool      `json:"needs_attention"` // the newest step failed, or its pull request didn't open
	UpdatedAt      time.Time `json:"updated_at"`
}

// listThreads serves GET /api/threads?limit=N, most recent activity first.
func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	threads, err := s.store.ListThreads(r.Context(), limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]threadJSON, len(threads))
	for i, th := range threads {
		l := th.Latest
		out[i] = threadJSON{ID: th.ID, Title: th.Title, Repo: th.Repo, Steps: th.Steps, Agents: th.Agents,
			Latest: toJSON(l), UpdatedAt: th.UpdatedAt.UTC(),
			NeedsAttention: l.State == task.Failed || (l.State == task.Succeeded && l.PR.Requested && l.PR.Error != "")}
	}
	writeData(w, http.StatusOK, out)
}

// SnapshotSettings are the snapshot limits the server runs with.
type SnapshotSettings struct {
	BudgetBytes int64 // 0 = no limit
}

// statusJSON is what the UI shows about the server as a whole.
type statusJSON struct {
	PullRequests struct {
		Enabled    bool   `json:"enabled"`
		GitHubUser string `json:"github_user"` // "" when disabled
	} `json:"pull_requests"`
	Snapshots struct {
		Enabled     bool  `json:"enabled"`
		Count       int   `json:"count"`
		Bytes       int64 `json:"bytes"`
		BudgetBytes int64 `json:"budget_bytes"` // 0 = no limit
	} `json:"snapshots"`
	Tasks struct {
		Queued  int `json:"queued"`
		Running int `json:"running"`
	} `json:"tasks"`
}

// status serves GET /api/status.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	var out statusJSON
	out.PullRequests.Enabled, out.PullRequests.GitHubUser = s.prs != nil, s.githubUser
	if s.snapshots != nil {
		snaps, err := s.store.ListSnapshots(r.Context())
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out.Snapshots.Enabled, out.Snapshots.Count, out.Snapshots.BudgetBytes = true, len(snaps), s.snapshots.BudgetBytes
		for _, sn := range snaps {
			out.Snapshots.Bytes += sn.SizeBytes
		}
	}
	counts, err := s.store.CountTasks(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out.Tasks.Queued, out.Tasks.Running = counts[task.Queued], counts[task.Running]
	writeData(w, http.StatusOK, out)
}

// getThread lists every task in the thread of the given task, oldest first.
func (s *Server) getThread(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.getRecord(w, r)
	if !ok {
		return
	}
	recs, err := s.store.ThreadTasks(r.Context(), rec.ThreadID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]taskJSON, len(recs))
	for i, rec := range recs {
		out[i] = toJSON(rec)
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	limit, ok := limitParam(w, r)
	if !ok {
		return
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
	path := rec.LogPath
	// A running task's log path is only recorded when it finishes, but the
	// runner always writes it to <out>/<id>/agent.log: serve it from there,
	// so clients can follow it live (with Range requests for the new part).
	if path == "" && rec.State == task.Running && s.outDir != "" {
		path = filepath.Join(s.outDir, rec.ID, "agent.log")
	}
	if path == "" {
		writeError(w, http.StatusNotFound, "no log for this task yet")
		return
	}
	s.serveFile(w, r, path, "text/plain; charset=utf-8")
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
// server's DefaultModel applies only to its default agent.
func (s *Server) defaultFamily(agentID string, cat *models.Catalog) string {
	if agentID == s.agent && s.defaultModel != "" {
		return s.defaultModel
	}
	return cat.DefaultFamily()
}

type agentJSON struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	DefaultModel string          `json:"default_model"` // model family
	Runnable     bool            `json:"runnable"`      // forgerun has an adapter for it (a credential is checked per task)
	Credential   *credentialJSON `json:"credential"`    // null when it isn't runnable
}

// credentialJSON says whether an agent's credential is configured. It's
// shown to the user; it never decides which agents are offered.
type credentialJSON struct {
	Name string `json:"name"` // e.g. "CLAUDE_CODE_OAUTH_TOKEN"; "" if the agent needs none
	Set  bool   `json:"set"`
}

// credential reports an agent's credential, or nil without an adapter.
func (s *Server) credential(ctx context.Context, agentID string) *credentialJSON {
	a, err := s.agents.Get(agentID)
	if err != nil {
		return nil
	}
	return &credentialJSON{Name: a.Credential(), Set: s.agents.Check(ctx, agentID) == nil}
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
		out = append(out, agentJSON{ID: a.ID, Name: a.Name, DefaultModel: s.defaultFamily(a.ID, cat), Runnable: s.runnable(a.ID),
			Credential: s.credential(r.Context(), a.ID)})
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
	}{agentID, s.runnable(agentID), s.defaultFamily(agentID, cat), out})
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
	case errors.Is(err, store.ErrThreadBusy), errors.Is(err, store.ErrNotLatest):
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
