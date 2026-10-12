package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/models"
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
	cfg := Config{Store: s, Pool: p, Agent: "cursor", Log: slog.New(slog.DiscardHandler)}
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
		"state", "error", "has_patch", "created_at", "started_at", "finished_at"} {
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
	if got.Agent != "other-agent" || got.Runnable || got.Default != "other-x" ||
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
		{ID: "other-agent", Name: "other-agent", DefaultModel: "other-x", Runnable: false},
	}
	if !slices.Equal(agents, want) {
		t.Errorf("agents = %+v\nwant     %+v", agents, want)
	}
}
