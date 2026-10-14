package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/task"
)

const secret = "github_pat_SECRET123"

// sh runs a shell script in dir and returns its output, failing the test on error.
func sh(t *testing.T, dir, script string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixture is a bare "remote" repo with two commits on main, and a patch
// (made like the runner makes one) against the first commit.
type fixture struct {
	remote, base, patch string
	pulls               []map[string]string // pull requests the fake API received
	mu                  sync.Mutex
	api                 *httptest.Server
	apiStatus           int // what POST /pulls answers; 0 = 201
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{remote: filepath.Join(root, "remote.git"), patch: filepath.Join(root, "changes.patch")}
	sh(t, root, "git init -q --bare -b main remote.git && git clone -q remote.git work")
	work := filepath.Join(root, "work")
	f.base = sh(t, work, `printf 'hello\n' > README.md && printf 'bye\n' > old.txt && printf 'x\ny\n' > rename-me.txt &&
		printf '#!/bin/sh\n' > run.sh && head -c 512 /dev/urandom > data.bin &&
		git add -A && git commit -qm base && git push -q origin HEAD:main && git rev-parse HEAD`)
	// The agent's changes: edit, add, delete, rename+edit, +x, binary.
	sh(t, work, `printf 'more\n' >> README.md && printf 'new\n' > new.txt && git rm -q old.txt &&
		git mv rename-me.txt renamed.txt && printf 'z\n' >> renamed.txt && chmod +x run.sh &&
		head -c 256 /dev/urandom > data.bin && git add -A && git diff --cached --binary > `+f.patch+` && git reset -q --hard`)
	// main moves on after the task started; the PR must still build on f.base.
	sh(t, work, `printf 'later\n' > later.txt && git add -A && git commit -qm later && git push -q origin HEAD:main`)

	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/pulls":
			if f.apiStatus != 0 {
				w.WriteHeader(f.apiStatus)
				fmt.Fprint(w, `{"message":"Validation Failed","errors":[{"message":"A pull request already exists for o:x."}]}`)
				return
			}
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			f.pulls = append(f.pulls, in)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"number":7,"html_url":"https://github.com/o/r/pull/7"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.api.Close)
	return f
}

func (f *fixture) publisher(token string) *Publisher {
	return &Publisher{
		Tokens:    forge.NewStaticToken(forge.NewToken(token)),
		GitHub:    &forge.GitHub{BaseURL: f.api.URL},
		RemoteURL: func(forge.Repo) string { return "file://" + f.remote },
		TaskURL:   func(id string) string { return "http://localhost:8080/api/tasks/" + id },
	}
}

func (f *fixture) request(summary string) task.PublishRequest {
	return task.PublishRequest{TaskID: "t-1", Repo: "https://github.com/o/r", BaseCommit: f.base,
		PatchPath: f.patch, Prompt: "Tidy up the files\nand more detail on a second line", Summary: summary}
}

func TestPublish(t *testing.T) {
	f := newFixture(t)
	url, err := f.publisher(secret).Publish(context.Background(), f.request("## Requested\nTidy up.\n\n## Done\nDid it.\n\n## Open\nNothing."))
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/o/r/pull/7" {
		t.Errorf("url = %q", url)
	}

	// The branch is on the remote: one commit on top of the task's base,
	// with every kind of change, by forgerun.
	check := t.TempDir()
	sh(t, check, "git clone -q "+f.remote+" . && git checkout -q forgerun/t-1")
	if parent := sh(t, check, "git rev-parse HEAD~1"); parent != f.base {
		t.Errorf("branch builds on %s, want the task's base %s", parent, f.base)
	}
	want := "A\tnew.txt\nD\told.txt\nM\tREADME.md\nM\tdata.bin\nM\trun.sh\nR\trename-me.txt\trenamed.txt"
	got := sh(t, check, `git diff --name-status -M HEAD~1 HEAD | sed 's/^R[0-9]*/R/' | LC_ALL=C sort`)
	if got != want {
		t.Errorf("changes on the branch:\n%s\nwant:\n%s", got, want)
	}
	if mode := sh(t, check, "git ls-files -s run.sh | cut -c1-6"); mode != "100755" {
		t.Errorf("run.sh mode = %s, want 100755", mode)
	}
	if who := sh(t, check, "git log -1 --format='%an <%ae>|%s'"); who != "forgerun <forgerun@users.noreply.github.com>|Tidy up the files" {
		t.Errorf("commit = %q", who)
	}

	// The pull request: from the branch into the default branch, titled by
	// the prompt's first line, described by the summary.
	if len(f.pulls) != 1 {
		t.Fatalf("%d pull requests opened", len(f.pulls))
	}
	pr := f.pulls[0]
	if pr["head"] != "forgerun/t-1" || pr["base"] != "main" || pr["title"] != "Tidy up the files" {
		t.Errorf("pull request = %v", pr)
	}
	for _, s := range []string{"## Done\nDid it.", "(http://localhost:8080/api/tasks/t-1)", "Original request", "> Tidy up the files"} {
		if !strings.Contains(pr["body"], s) {
			t.Errorf("body lacks %q:\n%s", s, pr["body"])
		}
	}
}

func TestPublishWithoutSummary(t *testing.T) {
	f := newFixture(t)
	if _, err := f.publisher(secret).Publish(context.Background(), f.request("")); err != nil {
		t.Fatal(err)
	}
	body := f.pulls[0]["body"]
	for _, s := range []string{"## Requested", "> Tidy up the files", "## Changed files", "new.txt"} {
		if !strings.Contains(body, s) {
			t.Errorf("fallback body lacks %q:\n%s", s, body)
		}
	}
}

func TestPublishFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("branch already exists: never overwritten", func(t *testing.T) {
		f := newFixture(t)
		sh(t, filepath.Dir(f.remote), "git -C work push -q origin HEAD:refs/heads/forgerun/t-1")
		_, err := f.publisher(secret).Publish(ctx, f.request(""))
		if err == nil || !strings.Contains(err.Error(), "git push") {
			t.Errorf("got %v, want a rejected push", err)
		}
	})
	t.Run("pull request API fails after push", func(t *testing.T) {
		f := newFixture(t)
		f.apiStatus = http.StatusUnprocessableEntity
		_, err := f.publisher(secret).Publish(ctx, f.request(""))
		if err == nil || !strings.Contains(err.Error(), "was pushed, but opening the pull request failed") ||
			!strings.Contains(err.Error(), "A pull request already exists") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("rejected token", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.publisher("github_pat_WRONG").Publish(ctx, f.request(""))
		if err == nil || !strings.Contains(err.Error(), "Bad credentials") || strings.Contains(err.Error(), "WRONG") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("unsupported host", func(t *testing.T) {
		f := newFixture(t)
		req := f.request("")
		req.Repo = "https://gitlab.com/o/r"
		if _, err := f.publisher(secret).Publish(ctx, req); err == nil || !strings.Contains(err.Error(), "aren't supported") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("token never appears in git errors", func(t *testing.T) {
		f := newFixture(t)
		p := f.publisher(secret)
		p.RemoteURL = func(forge.Repo) string { return "file:///nonexistent/" + secret }
		_, err := p.Publish(ctx, f.request(""))
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("got %v", err)
		}
	})
}

func TestTitle(t *testing.T) {
	long := strings.Repeat("é", 100)
	tests := map[string]string{
		"Fix the bug":                   "Fix the bug",
		"  Fix the bug\nwith details  ": "Fix the bug",
		long:                            strings.Repeat("é", 71) + "…",
	}
	for in, want := range tests {
		if got := title(in); got != want {
			t.Errorf("title(%.20q) = %q, want %q", in, got, want)
		}
	}
}
