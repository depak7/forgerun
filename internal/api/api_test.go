package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
	"github.com/depak7/forgerun/internal/worker"
)

// The real worker pool must satisfy the API's Pool interface.
var _ Pool = (*worker.Pool)(nil)

// cursorDefault is the default model of the "cursor" agent the API serves.
var cursorDefault = models.Variant{ID: "auto", AgentID: "cursor", Family: "auto", Name: "Auto", IsDefault: true, AgentDefault: true, ZDR: true}

// testModels are shaped like real seed rows, plus one of another agent.
var testModels = []models.Variant{
	cursorDefault,
	{ID: "claude-opus-5-5-medium", AgentID: "cursor", Family: "claude-opus-5-5", Name: "Claude Opus 5.5 1M", Effort: "medium", IsDefault: true, ZDR: true},
	{ID: "claude-opus-5-5-high", AgentID: "cursor", Family: "claude-opus-5-5", Name: "Claude Opus 5.5 1M High", Effort: "high", ZDR: true},
	{ID: "claude-opus-5-5-high-fast", AgentID: "cursor", Family: "claude-opus-5-5", Name: "Claude Opus 5.5 1M High Fast", Effort: "high", Fast: true, ZDR: true},
	{ID: "claude-fable-5-1-high", AgentID: "cursor", Family: "claude-fable-5-1", Name: "Claude Fable 5.1 1M (NO ZDR)", Effort: "high", IsDefault: true},
	{ID: "other-x", AgentID: "other-agent", Family: "other-x", Name: "Other", IsDefault: true, AgentDefault: true, ZDR: true},
}

type fakePool struct {
	nudges    int
	running   map[string]bool // tasks the fake pretends are running
	cancelled []string
}

func (p *fakePool) Nudge() { p.nudges++ }
func (p *fakePool) Cancel(id string) bool {
	if p.running[id] {
		p.cancelled = append(p.cancelled, id)
		return true
	}
	return false
}

// setup builds the API on a memory store; opts adjust the config.
func setup(t *testing.T, opts ...func(*Config)) (http.Handler, *memory.Store, *fakePool) {
	t.Helper()
	s, p := memory.New(testModels...), &fakePool{running: map[string]bool{}}
	cfg := Config{Store: s, Pool: p, Agent: "cursor", Log: slog.New(slog.DiscardHandler),
		Agents: agent.NewSet(agent.StaticCredentials{"CURSOR_API_KEY": "k"}, &agent.Cursor{}, otherAgent{})}
	for _, opt := range opts {
		opt(&cfg)
	}
	return New(cfg), s, p
}

func do(h http.Handler, method, path, body string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decode checks the envelope rules and returns data (success) or error (failure).
func decode(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) (data json.RawMessage, apiErr apiError) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status %d, want %d; body: %s", rec.Code, wantStatus, rec.Body)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body)
	}
	success := string(env["success"]) == "true"
	_, hasData := env["data"]
	_, hasError := env["error"]
	if success != (wantStatus < 400) || success == hasError || (success && !hasData) || (!success && hasData) {
		t.Fatalf("bad envelope for status %d: %s", wantStatus, rec.Body)
	}
	if hasError {
		if err := json.Unmarshal(env["error"], &apiErr); err != nil {
			t.Fatal(err)
		}
	}
	return env["data"], apiErr
}

func decodeTask(t *testing.T, data json.RawMessage) taskJSON {
	t.Helper()
	var tj taskJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		t.Fatal(err)
	}
	return tj
}

func createTask(t *testing.T, h http.Handler) taskJSON {
	t.Helper()
	data, _ := decode(t, do(h, "POST", "/api/tasks", `{"repo":"https://github.com/octocat/Hello-World","prompt":"hi"}`), http.StatusCreated)
	return decodeTask(t, data)
}

func TestCreate(t *testing.T) {
	h, _, p := setup(t)
	rec := do(h, "POST", "/api/tasks", `{"repo":"https://github.com/octocat/Hello-World","prompt":"add a line"}`)
	data, _ := decode(t, rec, http.StatusCreated)
	tj := decodeTask(t, data)

	if tj.State != task.Queued || tj.Prompt != "add a line" || tj.Agent != "cursor" {
		t.Errorf("created task: %+v", tj)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/tasks/"+tj.ID {
		t.Errorf("Location = %q", loc)
	}
	if p.nudges != 1 {
		t.Errorf("pool nudged %d times, want 1", p.nudges)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	tests := []struct{ name, body string }{
		{"not json", `{`},
		{"unknown field", `{"repo":"https://x.com/r","promt":"typo"}`},
		{"missing prompt", `{"repo":"https://x.com/r"}`},
		{"blank prompt", `{"repo":"https://x.com/r","prompt":"   "}`},
		{"file url", `{"repo":"file:///etc","prompt":"p"}`},
		{"ext transport", `{"repo":"ext::sh -c id","prompt":"p"}`},
		{"plain http", `{"repo":"http://x.com/r","prompt":"p"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, p := setup(t)
			_, apiErr := decode(t, do(h, "POST", "/api/tasks", tc.body), http.StatusBadRequest)
			if apiErr.Code != "invalid_request" || apiErr.Message == "" {
				t.Errorf("error = %+v", apiErr)
			}
			if p.nudges != 0 {
				t.Error("pool nudged for a rejected request")
			}
		})
	}

	t.Run("too large", func(t *testing.T) {
		h, _, _ := setup(t)
		body := `{"repo":"https://x.com/r","prompt":"` + strings.Repeat("a", 2<<20) + `"}`
		_, apiErr := decode(t, do(h, "POST", "/api/tasks", body), http.StatusRequestEntityTooLarge)
		if apiErr.Code != "payload_too_large" {
			t.Errorf("code = %q", apiErr.Code)
		}
	})
}

func TestGetAndNotFound(t *testing.T) {
	h, _, _ := setup(t)
	created := createTask(t, h)

	data, _ := decode(t, do(h, "GET", "/api/tasks/"+created.ID, ""), http.StatusOK)
	if decodeTask(t, data).ID != created.ID {
		t.Error("GET returned a different task")
	}
	_, apiErr := decode(t, do(h, "GET", "/api/tasks/bogus", ""), http.StatusNotFound)
	if apiErr.Code != "not_found" {
		t.Errorf("code = %q", apiErr.Code)
	}
}

func TestList(t *testing.T) {
	h, _, _ := setup(t)
	data, _ := decode(t, do(h, "GET", "/api/tasks", ""), http.StatusOK)
	if string(data) != "[]" {
		t.Errorf("empty list = %s, want []", data)
	}

	var ids []string
	for range 3 {
		ids = append(ids, createTask(t, h).ID)
		time.Sleep(2 * time.Millisecond)
	}
	data, _ = decode(t, do(h, "GET", "/api/tasks?limit=2", ""), http.StatusOK)
	var list []taskJSON
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != ids[2] || list[1].ID != ids[1] {
		t.Errorf("list = %+v", list)
	}

	for _, bad := range []string{"0", "101", "x"} {
		decode(t, do(h, "GET", "/api/tasks?limit="+bad, ""), http.StatusBadRequest)
	}
}

func TestCancel(t *testing.T) {
	h, s, p := setup(t)
	ctx := context.Background()

	t.Run("queued", func(t *testing.T) {
		tk := createTask(t, h)
		data, _ := decode(t, do(h, "POST", "/api/tasks/"+tk.ID+"/cancel", ""), http.StatusAccepted)
		if got := decodeTask(t, data); got.State != task.Cancelled {
			t.Errorf("state = %s, want cancelled", got.State)
		}
	})

	t.Run("running", func(t *testing.T) {
		tk := createTask(t, h)
		if err := s.Start(ctx, tk.ID); err != nil {
			t.Fatal(err)
		}
		p.running[tk.ID] = true
		decode(t, do(h, "POST", "/api/tasks/"+tk.ID+"/cancel", ""), http.StatusAccepted)
		if len(p.cancelled) != 1 || p.cancelled[0] != tk.ID {
			t.Errorf("pool cancelled %v, want [%s]", p.cancelled, tk.ID)
		}
	})

	t.Run("finished", func(t *testing.T) {
		tk := createTask(t, h)
		if err := s.Start(ctx, tk.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Finish(ctx, tk.ID, store.Outcome{State: task.Succeeded}); err != nil {
			t.Fatal(err)
		}
		_, apiErr := decode(t, do(h, "POST", "/api/tasks/"+tk.ID+"/cancel", ""), http.StatusConflict)
		if apiErr.Code != "conflict" || apiErr.Message != "task is already succeeded" {
			t.Errorf("error = %+v", apiErr)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		decode(t, do(h, "POST", "/api/tasks/bogus/cancel", ""), http.StatusNotFound)
	})
}

func TestLogAndPatch(t *testing.T) {
	h, s, _ := setup(t)
	ctx := context.Background()
	dir := t.TempDir()
	logPath, patchPath := filepath.Join(dir, "agent.log"), filepath.Join(dir, "changes.patch")
	os.WriteFile(logPath, []byte("line1\nline2\n"), 0o644)
	os.WriteFile(patchPath, []byte("+hello\n"), 0o644)

	done := createTask(t, h)
	s.Start(ctx, done.ID)
	s.Finish(ctx, done.ID, store.Outcome{State: task.Succeeded, LogPath: logPath, PatchPath: patchPath})

	rec := do(h, "GET", "/api/tasks/"+done.ID+"/log", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "line1\nline2\n" {
		t.Errorf("log: %d %q", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/tasks/"+done.ID+"/log", "", "Range", "bytes=6-")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "line2\n" {
		t.Errorf("log range: %d %q", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/tasks/"+done.ID+"/patch", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "+hello\n" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/x-diff") {
		t.Errorf("patch: %d %q %s", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}

	queued := createTask(t, h)
	_, apiErr := decode(t, do(h, "GET", "/api/tasks/"+queued.ID+"/patch", ""), http.StatusNotFound)
	if apiErr.Message != "task has not finished" {
		t.Errorf("patch on queued task: %q", apiErr.Message)
	}
	decode(t, do(h, "GET", "/api/tasks/"+queued.ID+"/log", ""), http.StatusNotFound)

	failed := createTask(t, h)
	s.Start(ctx, failed.ID)
	s.Finish(ctx, failed.ID, store.Outcome{State: task.Failed, Error: "boom"})
	_, apiErr = decode(t, do(h, "GET", "/api/tasks/"+failed.ID+"/patch", ""), http.StatusNotFound)
	if apiErr.Message != "task did not finish successfully" {
		t.Errorf("patch on failed task: %q", apiErr.Message)
	}
	decode(t, do(h, "GET", "/api/tasks/bogus/log", ""), http.StatusNotFound)
}

// downStore embeds a working store and overrides only Ping.
type downStore struct{ store.Store }

func (downStore) Ping(context.Context) error { return errors.New("connection refused") }

func TestHealth(t *testing.T) {
	h, _, _ := setup(t)
	decode(t, do(h, "GET", "/healthz", ""), http.StatusOK)

	down := New(Config{Store: downStore{memory.New(cursorDefault)}, Pool: &fakePool{}, Agent: "cursor", Log: slog.New(slog.DiscardHandler)})
	_, apiErr := decode(t, do(down, "GET", "/healthz", ""), http.StatusServiceUnavailable)
	if apiErr.Code != "unavailable" || strings.Contains(apiErr.Message, "refused") {
		t.Errorf("error = %+v (must not leak internal details)", apiErr)
	}
}

// The task shape is a contract with the UI: every field present, nulls explicit.
func TestTaskJSONShape(t *testing.T) {
	h, _, _ := setup(t)
	data, _ := decode(t, do(h, "GET", "/api/tasks/"+createTask(t, h).ID, ""), http.StatusOK)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "repo", "prompt", "agent", "model", "effort", "thinking", "fast", "model_id",
		"state", "error", "has_patch", "snapshot_id", "parent_id", "thread_id", "pr", "summary", "created_at", "started_at", "finished_at"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("task JSON missing %q", k)
		}
	}
	if !strings.HasSuffix(string(fields["created_at"]), `Z"`) {
		t.Errorf("created_at = %s, want UTC (…Z)", fields["created_at"])
	}
	if string(fields["model"]) != `"auto"` || string(fields["model_id"]) != `"auto"` {
		t.Errorf("model=%s model_id=%s, want the agent default auto", fields["model"], fields["model_id"])
	}
	if string(fields["started_at"]) != "null" || string(fields["state"]) != `"queued"` {
		t.Errorf("started_at=%s state=%s", fields["started_at"], fields["state"])
	}
	if _, ok := fields["log_path"]; ok {
		t.Error("server file paths must not be exposed")
	}
}

func TestLogRequests(t *testing.T) {
	var buf strings.Builder
	log := slog.New(slog.NewTextHandler(&buf, nil))
	h := LogRequests(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusTeapot, "short and stout")
	}))
	do(h, "GET", "/x", "")
	if !strings.Contains(buf.String(), "status=418") || !strings.Contains(buf.String(), "path=/x") {
		t.Errorf("log line: %s", buf.String())
	}
}

func TestCreateWithModel(t *testing.T) {
	h, _, _ := setup(t)
	data, _ := decode(t, do(h, "POST", "/api/tasks",
		`{"repo":"https://x.com/r","prompt":"p","model":"claude-opus-5-5","effort":"high","fast":true}`), http.StatusCreated)
	tj := decodeTask(t, data)
	if tj.ModelID != "claude-opus-5-5-high-fast" || tj.Model != "claude-opus-5-5" || tj.Effort != "high" || !tj.Fast || tj.Thinking {
		t.Errorf("task = %+v", tj)
	}
}

func TestCreateDefaults(t *testing.T) {
	h, _, _ := setup(t)
	if tj := createTask(t, h); tj.ModelID != "auto" {
		t.Errorf("no model, no server default: model_id = %q, want the agent default auto", tj.ModelID)
	}
	h, _, _ = setup(t, func(c *Config) { c.DefaultModel = "claude-opus-5-5" })
	if tj := createTask(t, h); tj.ModelID != "claude-opus-5-5-medium" {
		t.Errorf("server default: model_id = %q, want the family's default variant", tj.ModelID)
	}
}

func TestCreateRejectsBadModelChoice(t *testing.T) {
	tests := []struct{ name, fields, wantText string }{
		{"unknown model", `"model":"gpt-9"`, "unknown model"},
		{"other agent's model", `"model":"other-x"`, "unknown model"},
		{"bad effort", `"model":"claude-opus-5-5","effort":"ultra"`, "valid:"},
		{"no such combination", `"model":"claude-opus-5-5","effort":"medium","fast":true`, "valid:"},
		{"auto takes no options", `"effort":"high"`, "valid: default"},
		{"thinking not a bool", `"thinking":"yes"`, "invalid JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, s, _ := setup(t)
			body := `{"repo":"https://x.com/r","prompt":"p",` + tc.fields + `}`
			_, apiErr := decode(t, do(h, "POST", "/api/tasks", body), http.StatusBadRequest)
			if apiErr.Code != "invalid_request" || !strings.Contains(apiErr.Message, tc.wantText) {
				t.Errorf("error = %+v, want message containing %q", apiErr, tc.wantText)
			}
			if recs, _ := s.List(context.Background(), 10); len(recs) != 0 {
				t.Errorf("a rejected request created %d task(s)", len(recs))
			}
		})
	}
}

func TestListModels(t *testing.T) {
	h, _, _ := setup(t)
	data, _ := decode(t, do(h, "GET", "/api/models", ""), http.StatusOK)
	got := decodeModels(t, data)
	ids := familyIDs(got.Models)
	if got.Agent != "cursor" || !got.Runnable {
		t.Errorf("agent=%q runnable=%v, want cursor true", got.Agent, got.Runnable)
	}
	if got.Default != "auto" || !slices.Equal(ids, []string{"auto", "claude-fable-5-1", "claude-opus-5-5"}) {
		t.Fatalf("default=%q models=%v (another agent's model must not appear)", got.Default, ids)
	}
	if !strings.Contains(string(data), `"efforts":[]`) {
		t.Error("auto's efforts must encode as [], not null")
	}
	opus := got.Models[2]
	if !slices.Equal(opus.Efforts, []string{"medium", "high"}) || opus.DefaultEffort != "medium" || !opus.Fast || !opus.ZDR {
		t.Errorf("claude-opus-5-5 = %+v", opus)
	}

	h, _, _ = setup(t, func(c *Config) { c.DefaultModel = "claude-opus-5-5" })
	data, _ = decode(t, do(h, "GET", "/api/models", ""), http.StatusOK)
	if !strings.Contains(string(data), `"default":"claude-opus-5-5"`) {
		t.Errorf("server default not reported: %s", data)
	}
}

func TestRequireZDR(t *testing.T) {
	h, _, _ := setup(t, func(c *Config) { c.RequireZDR = true })
	decode(t, do(h, "POST", "/api/tasks", `{"repo":"https://x.com/r","prompt":"p","model":"claude-fable-5-1"}`), http.StatusBadRequest)
	data, _ := decode(t, do(h, "GET", "/api/models", ""), http.StatusOK)
	if strings.Contains(string(data), "claude-fable-5-1") {
		t.Error("non-ZDR model listed despite RequireZDR")
	}
	createTask(t, h) // ZDR models (auto) still work
}

type modelsResponse struct {
	Agent    string       `json:"agent"`
	Runnable bool         `json:"runnable"`
	Default  string       `json:"default"`
	Models   []familyJSON `json:"models"`
}

func decodeModels(t *testing.T, data json.RawMessage) modelsResponse {
	t.Helper()
	var got modelsResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func familyIDs(fams []familyJSON) []string {
	var ids []string
	for _, f := range fams {
		ids = append(ids, f.ID)
	}
	return ids
}

func TestListModelsForAgent(t *testing.T) {
	// The server default applies to its own agent only, never to another's listing.
	h, _, _ := setup(t, func(c *Config) { c.DefaultModel = "claude-opus-5-5" })
	data, _ := decode(t, do(h, "GET", "/api/models?agent=other-agent", ""), http.StatusOK)
	got := decodeModels(t, data)
	if got.Agent != "other-agent" || !got.Runnable || got.Default != "other-x" ||
		!slices.Equal(familyIDs(got.Models), []string{"other-x"}) {
		t.Errorf("other-agent listing = %+v", got)
	}

	_, apiErr := decode(t, do(h, "GET", "/api/models?agent=nope", ""), http.StatusNotFound)
	if apiErr.Code != "not_found" || apiErr.Message != "agent not found" {
		t.Errorf("unknown agent: %+v", apiErr)
	}

	down := New(Config{Store: disabledAgents{memory.New(testModels...)}, Pool: &fakePool{}, Agent: "cursor", Log: slog.New(slog.DiscardHandler)})
	decode(t, do(down, "GET", "/api/models?agent=cursor", ""), http.StatusNotFound)
}

// disabledAgents reports every agent as disabled.
type disabledAgents struct{ store.Store }

func (d disabledAgents) GetAgent(ctx context.Context, id string) (store.Agent, error) {
	a, err := d.Store.GetAgent(ctx, id)
	a.Enabled = false
	return a, err
}

func TestListAgents(t *testing.T) {
	h, _, _ := setup(t)
	data, _ := decode(t, do(h, "GET", "/api/agents", ""), http.StatusOK)
	var agents []agentJSON
	if err := json.Unmarshal(data, &agents); err != nil {
		t.Fatal(err)
	}
	want := []agentJSON{
		{ID: "cursor", Name: "cursor", DefaultModel: "auto", Runnable: true},
		{ID: "other-agent", Name: "other-agent", DefaultModel: "other-x", Runnable: true},
	}
	for i := range agents {
		agents[i].Credential = nil // checked in TestAgentCredentialStatus
	}
	if !slices.Equal(agents, want) {
		t.Errorf("agents = %+v\nwant     %+v", agents, want)
	}

	// An agent in the database that forgerun has no adapter for is listed,
	// but not runnable.
	h, _, _ = setup(t, func(c *Config) { c.Agents = agent.NewSet(nil, &agent.Cursor{}) })
	json.Unmarshal(mustData(t, do(h, "GET", "/api/agents", ""), http.StatusOK), &agents)
	if !slices.ContainsFunc(agents, func(a agentJSON) bool { return a.ID == "other-agent" && !a.Runnable }) {
		t.Errorf("agent without an adapter: %+v", agents)
	}
}

func TestCreateWithPullRequest(t *testing.T) {
	const body = `{"repo":"https://github.com/o/r","prompt":"fix it","pr":true}`

	// Without a GitHub token, refuse up front instead of failing after the work.
	h, _, _ := setup(t)
	_, apiErr := decode(t, do(h, "POST", "/api/tasks", body), http.StatusBadRequest)
	if !strings.Contains(apiErr.Message, "pull requests are disabled") {
		t.Errorf("no token: %q", apiErr.Message)
	}

	h, s, _ := setup(t, func(c *Config) { c.PRs = fakePRs{open: true} })
	_, apiErr = decode(t, do(h, "POST", "/api/tasks", `{"repo":"https://gitlab.com/o/r","prompt":"fix it","pr":true}`), http.StatusBadRequest)
	if !strings.Contains(apiErr.Message, "aren't supported") {
		t.Errorf("unsupported host: %q", apiErr.Message)
	}

	data, _ := decode(t, do(h, "POST", "/api/tasks", body), http.StatusCreated)
	tk := decodeTask(t, data)
	if !tk.PR.Requested || tk.PR.URL != "" || tk.PR.Error != "" || tk.Summary != "" {
		t.Errorf("new PR task: pr=%+v summary=%q", tk.PR, tk.Summary)
	}
	if rec, _ := s.Get(context.Background(), tk.ID); !rec.PR.Requested {
		t.Error("store didn't record the PR request")
	}

	// Without "pr", nothing changes.
	data, _ = decode(t, do(h, "POST", "/api/tasks", `{"repo":"https://gitlab.com/o/r","prompt":"fix it"}`), http.StatusCreated)
	if tk := decodeTask(t, data); tk.PR.Requested {
		t.Error("PR requested without asking")
	}
}

// fakePRs answers whether a pull request is open.
type fakePRs struct {
	open bool
	err  error
}

func (f fakePRs) IsOpen(context.Context, string, int) (bool, error) { return f.open, f.err }

// finishWithPR moves a task to finished with an opened pull request, as a
// worker would.
func finishWithPR(t *testing.T, s *memory.Store, id string, state task.State) {
	t.Helper()
	ctx := context.Background()
	if err := s.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	o := store.Outcome{State: state, Summary: "summary of " + id}
	if state == task.Succeeded {
		o.PRURL, o.PRNumber, o.PRBranch = "https://github.com/o/r/pull/9", 9, "forgerun/"+id
	}
	if err := s.Finish(ctx, id, o); err != nil {
		t.Fatal(err)
	}
}

func TestFollowUps(t *testing.T) {
	h, s, pool := setup(t, func(c *Config) { c.PRs = fakePRs{open: true} })
	first := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks", `{"repo":"https://github.com/o/r","prompt":"first","pr":true}`), http.StatusCreated))
	follow := func(id, body string) *httptest.ResponseRecorder {
		return do(h, "POST", "/api/tasks/"+id+"/followups", body)
	}

	// Not before it has finished and opened its pull request.
	_, apiErr := decode(t, follow(first.ID, `{"prompt":"more"}`), http.StatusConflict)
	if !strings.Contains(apiErr.Message, "no pull request") {
		t.Errorf("before PR: %q", apiErr.Message)
	}
	finishWithPR(t, s, first.ID, task.Succeeded)

	nudges := pool.nudges
	second := decodeTask(t, mustData(t, follow(first.ID, `{"prompt":"second","model":"claude-opus-5-5","effort":"high"}`), http.StatusCreated))
	if second.ParentID == nil || *second.ParentID != first.ID || second.ThreadID != first.ID ||
		!second.PR.Requested || second.PR.Number != 9 || second.PR.Branch != "forgerun/"+first.ID ||
		second.Repo != first.Repo || second.ModelID != "claude-opus-5-5-high" {
		t.Errorf("follow-up = %+v", second)
	}
	if pool.nudges != nudges+1 {
		t.Error("worker pool not nudged")
	}

	// The thread runs one task at a time, and continues from its latest task.
	_, apiErr = decode(t, follow(second.ID, `{"prompt":"third"}`), http.StatusConflict)
	if !strings.Contains(apiErr.Message, "still queued") {
		t.Errorf("busy thread: %q", apiErr.Message)
	}
	finishWithPR(t, s, second.ID, task.Failed) // a failed follow-up can still be continued
	_, apiErr = decode(t, follow(first.ID, `{"prompt":"third"}`), http.StatusConflict)
	if !strings.Contains(apiErr.Message, "latest task in this thread: "+second.ID) {
		t.Errorf("not latest: %q", apiErr.Message)
	}
	third := decodeTask(t, mustData(t, follow(second.ID, `{"prompt":"third"}`), http.StatusCreated))
	if third.ThreadID != first.ID || third.PR.Number != 9 {
		t.Errorf("third = %+v", third)
	}

	// The thread lists all three, oldest first.
	var thread []taskJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/tasks/"+third.ID+"/thread", ""), http.StatusOK), &thread)
	if len(thread) != 3 || thread[0].ID != first.ID || thread[1].ID != second.ID || thread[2].ID != third.ID {
		t.Errorf("thread = %v", thread)
	}
	if first.ParentID != nil || first.ThreadID != first.ID {
		t.Errorf("first task: parent %v, thread %q", first.ParentID, first.ThreadID)
	}
}

func TestFollowUpRefusals(t *testing.T) {
	prepare := func(t *testing.T, prs PRs) (http.Handler, string) {
		h, s, _ := setup(t, func(c *Config) { c.PRs = prs })
		tk := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks", `{"repo":"https://github.com/o/r","prompt":"first","pr":true}`), http.StatusCreated))
		finishWithPR(t, s, tk.ID, task.Succeeded)
		return h, tk.ID
	}
	tests := []struct {
		name, body string
		prs        PRs
		status     int
		want       string
	}{
		{"closed pull request", `{"prompt":"x"}`, fakePRs{open: false}, http.StatusConflict, "closed or merged"},
		{"GitHub unreachable", `{"prompt":"x"}`, fakePRs{err: errors.New("dial tcp: timeout")}, http.StatusBadGateway, "timeout"},
		{"agent without an adapter", `{"prompt":"x","agent":"no-such-agent"}`, fakePRs{open: true}, http.StatusBadRequest, "isn't available"},
		{"agent without a credential", `{"prompt":"x","agent":"other-agent"}`, fakePRs{open: true}, http.StatusBadRequest, "set OTHER_KEY"},
		{"no prompt", `{"prompt":" "}`, fakePRs{open: true}, http.StatusBadRequest, "prompt is required"},
		{"bad model", `{"prompt":"x","model":"nope"}`, fakePRs{open: true}, http.StatusBadRequest, "nope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, id := prepare(t, tc.prs)
			_, apiErr := decode(t, do(h, "POST", "/api/tasks/"+id+"/followups", tc.body), tc.status)
			if !strings.Contains(apiErr.Message, tc.want) {
				t.Errorf("got %q, want it to mention %q", apiErr.Message, tc.want)
			}
		})
	}
	t.Run("pull requests off", func(t *testing.T) {
		h, _, _ := setup(t)
		_, apiErr := decode(t, do(h, "POST", "/api/tasks/t-x/followups", `{"prompt":"x"}`), http.StatusBadRequest)
		if !strings.Contains(apiErr.Message, "disabled") {
			t.Errorf("got %q", apiErr.Message)
		}
	})
	t.Run("unknown task", func(t *testing.T) {
		h, _, _ := setup(t, func(c *Config) { c.PRs = fakePRs{open: true} })
		decode(t, do(h, "POST", "/api/tasks/t-nope/followups", `{"prompt":"x"}`), http.StatusNotFound)
	})
}

// mustData returns a successful response's data.
func mustData(t *testing.T, rec *httptest.ResponseRecorder, status int) json.RawMessage {
	t.Helper()
	data, _ := decode(t, rec, status)
	return data
}

// otherAgent is a second agent, whose credential is OTHER_KEY.
type otherAgent struct{}

func (otherAgent) ID() string         { return "other-agent" }
func (otherAgent) Image() string      { return "img" }
func (otherAgent) Credential() string { return "OTHER_KEY" }
func (otherAgent) Secrets(v string) []sandbox.Secret {
	return []sandbox.Secret{{EnvVar: "OTHER_KEY", Value: v}}
}
func (otherAgent) StatePaths() []string { return nil }
func (otherAgent) Run(context.Context, *sandbox.Sandbox, agent.Request, io.Writer) error {
	return nil
}

// The request picks the agent; a missing credential refuses only tasks for
// that agent, at creation, and never hides the agent.
func TestCreateOnChosenAgent(t *testing.T) {
	h, s, _ := setup(t)
	_, apiErr := decode(t, do(h, "POST", "/api/tasks", `{"repo":"https://x.com/r","prompt":"p","agent":"other-agent"}`), http.StatusBadRequest)
	if !strings.Contains(apiErr.Message, "no credential configured for other-agent: set OTHER_KEY") {
		t.Errorf("no credential: %q", apiErr.Message)
	}
	// Still listed and runnable: a missing key hides nothing.
	var agents []agentJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/agents", ""), http.StatusOK), &agents)
	if !slices.ContainsFunc(agents, func(a agentJSON) bool { return a.ID == "other-agent" && a.Runnable }) {
		t.Errorf("other-agent hidden or not runnable: %+v", agents)
	}

	h, s, _ = setup(t, func(c *Config) {
		c.Agents = agent.NewSet(agent.StaticCredentials{"CURSOR_API_KEY": "k", "OTHER_KEY": "o"}, &agent.Cursor{}, otherAgent{})
	})
	tk := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks", `{"repo":"https://x.com/r","prompt":"p","agent":"other-agent"}`), http.StatusCreated))
	if tk.Agent != "other-agent" || tk.ModelID != "other-x" {
		t.Errorf("task on agent %q, model %q; want other-agent, its default other-x", tk.Agent, tk.ModelID)
	}
	if rec, _ := s.Get(context.Background(), tk.ID); rec.AgentID != "other-agent" {
		t.Errorf("stored agent %q", rec.AgentID)
	}
	// Models are resolved against the chosen agent.
	_, apiErr = decode(t, do(h, "POST", "/api/tasks", `{"repo":"https://x.com/r","prompt":"p","agent":"other-agent","model":"claude-opus-5-5"}`), http.StatusBadRequest)
	if apiErr.Message == "" {
		t.Error("another agent's model was accepted")
	}
	// Without "agent", the server's default agent.
	if tk := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks", `{"repo":"https://x.com/r","prompt":"p"}`), http.StatusCreated)); tk.Agent != "cursor" {
		t.Errorf("default agent = %q", tk.Agent)
	}
	_, apiErr = decode(t, do(h, "POST", "/api/tasks", `{"repo":"https://x.com/r","prompt":"p","agent":"no-such-agent"}`), http.StatusBadRequest)
	if !strings.Contains(apiErr.Message, "isn't available") {
		t.Errorf("unknown agent: %q", apiErr.Message)
	}
}

// A thread can switch agents; a follow-up without "agent" keeps the
// previous task's.
func TestFollowUpSwitchesAgent(t *testing.T) {
	h, s, _ := setup(t, func(c *Config) {
		c.PRs = fakePRs{open: true}
		c.Agents = agent.NewSet(agent.StaticCredentials{"CURSOR_API_KEY": "k", "OTHER_KEY": "o"}, &agent.Cursor{}, otherAgent{})
	})
	first := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks", `{"repo":"https://github.com/o/r","prompt":"first","pr":true}`), http.StatusCreated))
	finishWithPR(t, s, first.ID, task.Succeeded)
	second := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks/"+first.ID+"/followups", `{"prompt":"second","agent":"other-agent"}`), http.StatusCreated))
	if second.Agent != "other-agent" || second.ModelID != "other-x" || second.ThreadID != first.ID {
		t.Errorf("switched follow-up = agent %q model %q thread %q", second.Agent, second.ModelID, second.ThreadID)
	}
	finishWithPR(t, s, second.ID, task.Succeeded)
	third := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks/"+second.ID+"/followups", `{"prompt":"third"}`), http.StatusCreated))
	if third.Agent != "other-agent" {
		t.Errorf("follow-up without agent ran on %q, want the previous task's other-agent", third.Agent)
	}
}

// A running task's log is served before the task records where it is, so it
// can be followed live; a Range request returns only the new part.
func TestLogWhileRunning(t *testing.T) {
	out := t.TempDir()
	h, s, _ := setup(t, func(c *Config) { c.OutDir = out })
	tk := createTask(t, h)
	decode(t, do(h, "GET", "/api/tasks/"+tk.ID+"/log", ""), http.StatusNotFound) // queued: nothing yet

	if err := s.Start(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(out, tk.ID), 0o755)
	os.WriteFile(filepath.Join(out, tk.ID, "agent.log"), []byte("==> cloning\n==> running cursor agent\n"), 0o644)

	rec := do(h, "GET", "/api/tasks/"+tk.ID+"/log", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "running cursor agent") {
		t.Fatalf("running task's log: %d %q", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/tasks/"+tk.ID+"/log", "", "Range", "bytes=12-")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "==> running cursor agent\n" {
		t.Errorf("range: %d %q", rec.Code, rec.Body)
	}
}

func TestListThreads(t *testing.T) {
	h, s, _ := setup(t, func(c *Config) { c.PRs = fakePRs{open: true} })
	ctx := context.Background()
	first := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks", `{"repo":"https://github.com/o/r","prompt":"first","pr":true}`), http.StatusCreated))
	finishWithPR(t, s, first.ID, task.Succeeded)
	follow := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks/"+first.ID+"/followups", `{"prompt":"second"}`), http.StatusCreated))
	finishWithPR(t, s, follow.ID, task.Failed)
	// A task that succeeded but whose pull request didn't open also needs attention.
	lonely := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks", `{"repo":"https://github.com/o/r","prompt":"lonely","pr":true}`), http.StatusCreated))
	s.Start(ctx, lonely.ID)
	s.Finish(ctx, lonely.ID, store.Outcome{State: task.Succeeded, PRError: "the token needs: contents=write"})

	var threads []threadJSON
	if err := json.Unmarshal(mustData(t, do(h, "GET", "/api/threads", ""), http.StatusOK), &threads); err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 {
		t.Fatalf("%d threads: %+v", len(threads), threads)
	}
	byID := map[string]threadJSON{threads[0].ID: threads[0], threads[1].ID: threads[1]}
	th := byID[first.ID]
	if th.Title != "first" || th.Steps != 2 || th.Latest.ID != follow.ID || th.Latest.State != task.Failed ||
		!th.NeedsAttention || th.Latest.PR.Number != 9 || len(th.Agents) != 2 {
		t.Errorf("thread = %+v", th)
	}
	if l := byID[lonely.ID]; !l.NeedsAttention || l.Latest.PR.Error == "" {
		t.Errorf("succeeded without its PR should need attention: %+v", l)
	}
	decode(t, do(h, "GET", "/api/threads?limit=0", ""), http.StatusBadRequest)
}

func TestStatus(t *testing.T) {
	h, s, _ := setup(t, func(c *Config) {
		c.PRs, c.GitHubUser = fakePRs{open: true}, "depak7"
		c.Snapshots = &SnapshotSettings{BudgetBytes: 20e9}
	})
	ctx := context.Background()
	s.ReplaceSnapshot(ctx, store.Snapshot{Repo: "https://github.com/o/r", AgentID: "cursor", Ref: "/a", Commit: "c", SizeBytes: 6e6})
	running := createTask(t, h)
	s.Start(ctx, running.ID)
	createTask(t, h) // queued

	var st statusJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/status", ""), http.StatusOK), &st)
	if !st.PullRequests.Enabled || st.PullRequests.GitHubUser != "depak7" {
		t.Errorf("pull requests = %+v", st.PullRequests)
	}
	if !st.Snapshots.Enabled || st.Snapshots.Count != 1 || st.Snapshots.Bytes != 6e6 || st.Snapshots.BudgetBytes != 20e9 {
		t.Errorf("snapshots = %+v", st.Snapshots)
	}
	if st.Tasks.Running != 1 || st.Tasks.Queued != 1 {
		t.Errorf("tasks = %+v", st.Tasks)
	}

	// PRs and snapshots off: reported as such.
	h, _, _ = setup(t)
	json.Unmarshal(mustData(t, do(h, "GET", "/api/status", ""), http.StatusOK), &st)
	if st.PullRequests.Enabled || st.PullRequests.GitHubUser != "" || st.Snapshots.Enabled {
		t.Errorf("all off: %+v", st)
	}
}

// Agents show whether their credential is set; it never hides them.
func TestAgentCredentialStatus(t *testing.T) {
	h, _, _ := setup(t, func(c *Config) {
		c.Agents = agent.NewSet(agent.StaticCredentials{"CURSOR_API_KEY": "k"}, &agent.Cursor{}, otherAgent{})
	})
	var agents []agentJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/agents", ""), http.StatusOK), &agents)
	want := map[string]credentialJSON{"cursor": {Name: "CURSOR_API_KEY", Set: true}, "other-agent": {Name: "OTHER_KEY", Set: false}}
	for _, a := range agents {
		if a.Credential == nil || *a.Credential != want[a.ID] || !a.Runnable {
			t.Errorf("%s: credential %+v, runnable %v", a.ID, a.Credential, a.Runnable)
		}
	}
	// No adapter: listed, not runnable, no credential to report.
	h, _, _ = setup(t, func(c *Config) { c.Agents = agent.NewSet(nil, &agent.Cursor{}) })
	json.Unmarshal(mustData(t, do(h, "GET", "/api/agents", ""), http.StatusOK), &agents)
	for _, a := range agents {
		if a.ID == "other-agent" && (a.Runnable || a.Credential != nil) {
			t.Errorf("agent without an adapter: %+v", a)
		}
	}
}

// The UI shows a running task's latest log line by asking for just the end
// of the file: a suffix range.
func TestLogTail(t *testing.T) {
	out := t.TempDir()
	h, s, _ := setup(t, func(c *Config) { c.OutDir = out })
	tk := createTask(t, h)
	s.Start(context.Background(), tk.ID)
	os.MkdirAll(filepath.Join(out, tk.ID), 0o755)
	os.WriteFile(filepath.Join(out, tk.ID, "agent.log"), []byte("==> cloning\n==> running cursor agent\nEdited README.md\n"), 0o644)

	rec := do(h, "GET", "/api/tasks/"+tk.ID+"/log", "", "Range", "bytes=-17")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "Edited README.md\n" {
		t.Errorf("tail: %d %q", rec.Code, rec.Body)
	}
}

func TestListSnapshots(t *testing.T) {
	h, s, _ := setup(t, func(c *Config) { c.Snapshots = &SnapshotSettings{} })
	s.ReplaceSnapshot(context.Background(), store.Snapshot{Repo: "https://GitHub.com/depak7/playground.git", AgentID: "cursor", Ref: "/a", Commit: "ccbf076", SizeBytes: 6e6})
	var snaps []snapshotJSON
	json.Unmarshal(mustData(t, do(h, "GET", "/api/snapshots", ""), http.StatusOK), &snaps)
	if len(snaps) != 1 || snaps[0].Repo != "https://github.com/depak7/playground" || snaps[0].Agent != "cursor" ||
		snaps[0].Commit != "ccbf076" || snaps[0].State != "current" || snaps[0].SizeBytes != 6e6 {
		t.Errorf("snapshots = %+v", snaps)
	}
	// Snapshots off: an empty list, not null.
	h, _, _ = setup(t)
	rec := do(h, "GET", "/api/snapshots", "")
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Errorf("off: %s", rec.Body)
	}
}
