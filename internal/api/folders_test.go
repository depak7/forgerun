package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/task"
)

// folderTar is a gzipped tar of the files given (name → content).
func folderTar(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.String()
}

func uploadOK(t *testing.T, h http.Handler, name, body string) folderJSON {
	t.Helper()
	var f folderJSON
	json.Unmarshal(mustData(t, do(h, "POST", "/api/folders?name="+name, body), http.StatusCreated), &f)
	return f
}

// A task can start from an uploaded folder instead of a repo; its thread
// shows the folder's name, and follow-ups stay on the folder.
func TestFolderTask(t *testing.T) {
	h, s, _ := setup(t, func(c *Config) { c.PRs = fakePRs{open: true} })
	f := uploadOK(t, h, "my-app", folderTar(t, map[string]string{"main.go": "package main\n"}))
	if !strings.HasPrefix(f.ID, "f-") || f.Name != "my-app" || f.SizeBytes == 0 {
		t.Fatalf("uploaded %+v", f)
	}

	// With PRs on, a repo task is checked against GitHub; a folder's isn't.
	tk := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks", `{"folder":"`+f.ID+`","prompt":"p"}`), http.StatusCreated))
	if tk.Repo != "my-app" || tk.Folder != f.ID {
		t.Errorf("task repo %q, folder %q", tk.Repo, tk.Folder)
	}
	s.Start(t.Context(), tk.ID)
	s.Finish(t.Context(), tk.ID, store.Outcome{State: task.Succeeded, Diff: "+a\n"})
	next := decodeTask(t, mustData(t, do(h, "POST", "/api/tasks/"+tk.ID+"/followups", `{"prompt":"more"}`), http.StatusCreated))
	if next.Folder != f.ID {
		t.Errorf("follow-up folder %q, want %q", next.Folder, f.ID)
	}

	// Its changes come from the diff: there's no branch to push to yet.
	_, apiErr := decode(t, do(h, "POST", "/api/threads/"+tk.ID+"/push", `{}`), http.StatusBadRequest)
	if !strings.Contains(apiErr.Message, "can't be pushed yet") {
		t.Errorf("push: %q", apiErr.Message)
	}
}

func TestFolderUploadRefusals(t *testing.T) {
	h, _, _ := setup(t, func(c *Config) { c.MaxFolder = 1 << 10 })
	ok := folderTar(t, map[string]string{"a": "a"})
	tests := []struct {
		name, path, body string
		status           int
		want             string
	}{
		{"no name", "/api/folders", ok, http.StatusBadRequest, "name is required"},
		{"a path as the name", "/api/folders?name=..", ok, http.StatusBadRequest, "name is required"},
		{"not a tarball", "/api/folders?name=x", "hello", http.StatusBadRequest, "gzipped tar"},
		{"outside the folder", "/api/folders?name=x", folderTar(t, map[string]string{"../evil": "x"}), http.StatusBadRequest, "outside the folder"},
		{"too big", "/api/folders?name=x", strings.Repeat("x", 2<<10), http.StatusRequestEntityTooLarge, "larger than"},
		{"unpacks too big", "/api/folders?name=x", folderTar(t, map[string]string{"zeros": strings.Repeat("\x00", 200<<10)}), http.StatusBadRequest, "unpacks to more than"},
	}
	for _, tc := range tests {
		_, apiErr := decode(t, do(h, "POST", tc.path, tc.body), tc.status)
		if !strings.Contains(apiErr.Message, tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, apiErr.Message, tc.want)
		}
	}
	// A path given as the name keeps only its last part.
	if f := uploadOK(t, h, "/Users/me/src/my-app", ok); f.Name != "my-app" {
		t.Errorf("name %q", f.Name)
	}
}

func TestFolderTaskRefusals(t *testing.T) {
	h, _, _ := setup(t)
	f := uploadOK(t, h, "x", folderTar(t, map[string]string{"a": "a"}))
	_, apiErr := decode(t, do(h, "POST", "/api/tasks", `{"folder":"`+f.ID+`","repo":"https://github.com/o/r","prompt":"p"}`), http.StatusBadRequest)
	if !strings.Contains(apiErr.Message, "not both") {
		t.Errorf("both: %q", apiErr.Message)
	}
	decode(t, do(h, "POST", "/api/tasks", `{"folder":"f-nope","prompt":"p"}`), http.StatusNotFound)
}

// Separate workers download the folder from the orchestrator, so folders
// work with them too.
func TestFolderTaskWithWorkers(t *testing.T) {
	h, _, _ := setup(t, func(c *Config) { c.Workers = fakeWorkers{{Name: "w", Agents: []string{"cursor"}}} })
	f := uploadOK(t, h, "x", folderTar(t, map[string]string{"a": "a"}))
	mustData(t, do(h, "POST", "/api/tasks", `{"folder":"`+f.ID+`","prompt":"p"}`), http.StatusCreated)
}
