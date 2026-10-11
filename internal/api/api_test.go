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
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
	"github.com/depak7/forgerun/internal/task"
	"github.com/depak7/forgerun/internal/worker"
)

// The real worker pool must satisfy the API's Pool interface.
var _ Pool = (*worker.Pool)(nil)

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

func setup(t *testing.T) (http.Handler, *memory.Store, *fakePool) {
	t.Helper()
	s, p := memory.New(), &fakePool{running: map[string]bool{}}
	return New(s, p, "cursor", slog.New(slog.DiscardHandler)), s, p
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
		if apiErr.Code != "conflict" {
			t.Errorf("code = %q", apiErr.Code)
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
	decode(t, do(h, "GET", "/api/tasks/bogus/log", ""), http.StatusNotFound)
}

// downStore embeds a working store and overrides only Ping.
type downStore struct{ store.Store }

func (downStore) Ping(context.Context) error { return errors.New("connection refused") }

func TestHealth(t *testing.T) {
	h, _, _ := setup(t)
	decode(t, do(h, "GET", "/healthz", ""), http.StatusOK)

	down := New(downStore{memory.New()}, &fakePool{}, "cursor", slog.New(slog.DiscardHandler))
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
	for _, k := range []string{"id", "repo", "prompt", "agent", "state", "error", "has_patch", "created_at", "started_at", "finished_at"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("task JSON missing %q", k)
		}
	}
	if string(fields["started_at"]) != "null" || string(fields["state"]) != `"queued"` {
		t.Errorf("started_at=%s state=%s", fields["started_at"], fields["state"])
	}
	if _, ok := fields["log_path"]; ok {
		t.Error("server file paths must not be exposed")
	}
}
