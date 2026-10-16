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
	updates             []map[string]string // pull request edits it received
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
		case r.Method == "PATCH" && r.URL.Path == "/repos/o/r/pulls/7":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			f.updates = append(f.updates, in)
			f.mu.Unlock()
			fmt.Fprint(w, `{"number":7,"html_url":"https://github.com/o/r/pull/7","state":"open"}`)
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/7":
			fmt.Fprint(w, `{"number":7,"html_url":"https://github.com/o/r/pull/7","state":"open"}`)
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls/8":
			fmt.Fprint(w, `{"number":8,"state":"closed","merged":true}`)
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
		ThreadURL: func(id string) string { return "http://localhost:8080/threads/" + id },
	}
}

// read returns a file's contents, failing the test if it can't.
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const prompt = "Tidy up the files\nand more detail on a second line"

// request asks for the fixture's changes on a new branch with a pull request.
func (f *fixture) request(t *testing.T, summary string) task.PublishRequest {
	return task.PublishRequest{ThreadID: "t-20261015-1", Repo: "https://github.com/o/r", BaseCommit: f.base,
		Patches: []string{read(t, f.patch)}, Requests: []string{prompt}, Summary: summary, OpenPR: true}
}

func TestBranch(t *testing.T) {
	tests := []struct{ id, prompt, want string }{
		{"t-20261015-224534-9fe9cc", "Add a hello world program in Go", "forgerun/add-a-hello-world-program-in-go-9fe9cc"},
		{"t-1-abcdef", "Fix the `parseDate()` bug!\nDetails: it's off by one.", "forgerun/fix-the-parsedate-bug-details-it-s-off-abcdef"},
		{"t-1-abcdef", "Ünïcode only: 日本語", "forgerun/n-code-only-abcdef"},
		{"t-1-abcdef", "!!!", "forgerun/abcdef"},
		{"t-1-abcdef", strings.Repeat("x", 60), "forgerun/" + strings.Repeat("x", 40) + "-abcdef"},
	}
	for _, tc := range tests {
		if got := Branch(tc.id, tc.prompt); got != tc.want {
			t.Errorf("Branch(%q, %q) = %q, want %q", tc.id, tc.prompt, got, tc.want)
		}
	}
}

func TestPublish(t *testing.T) {
	f := newFixture(t)
	pr, err := f.publisher(secret).Publish(context.Background(), f.request(t, "## Requested\nTidy up.\n\n## Done\nDid it.\n\n## Open\nNothing."))
	if err != nil {
		t.Fatal(err)
	}
	branch := Branch("t-20261015-1", prompt)
	if pr != (task.Published{PRURL: "https://github.com/o/r/pull/7", PRNumber: 7, Branch: branch}) {
		t.Errorf("published %+v", pr)
	}

	// The branch is on the remote: one commit on top of the task's base,
	// with every kind of change, by forgerun.
	check := t.TempDir()
	sh(t, check, "git clone -q "+f.remote+" . && git checkout -q "+branch)
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
	opened := f.pulls[0]
	if opened["head"] != branch || opened["base"] != "main" || opened["title"] != "Tidy up the files" {
		t.Errorf("pull request = %v", opened)
	}
	for _, s := range []string{"## Done\nDid it.", "(http://localhost:8080/threads/t-20261015-1)", "Original request", "> Tidy up the files"} {
		if !strings.Contains(opened["body"], s) {
			t.Errorf("body lacks %q:\n%s", s, opened["body"])
		}
	}
}

func TestPublishWithoutSummary(t *testing.T) {
	f := newFixture(t)
	if _, err := f.publisher(secret).Publish(context.Background(), f.request(t, "")); err != nil {
		t.Fatal(err)
	}
	body := f.pulls[0]["body"]
	for _, s := range []string{"## Requested", "> Tidy up the files", "## Changed in the latest commit", "new.txt"} {
		if !strings.Contains(body, s) {
			t.Errorf("fallback body lacks %q:\n%s", s, body)
		}
	}
}

func TestPublishFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("branch already exists: never overwritten", func(t *testing.T) {
		f := newFixture(t)
		sh(t, filepath.Dir(f.remote), "git -C work push -q origin HEAD:refs/heads/"+Branch("t-20261015-1", prompt))
		_, err := f.publisher(secret).Publish(ctx, f.request(t, ""))
		if err == nil || !strings.Contains(err.Error(), "git push") {
			t.Errorf("got %v, want a rejected push", err)
		}
	})
	t.Run("pull request API fails after push", func(t *testing.T) {
		f := newFixture(t)
		f.apiStatus = http.StatusUnprocessableEntity
		_, err := f.publisher(secret).Publish(ctx, f.request(t, ""))
		if err == nil || !strings.Contains(err.Error(), "is pushed, but opening the pull request failed") ||
			!strings.Contains(err.Error(), "A pull request already exists") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("rejected token", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.publisher("github_pat_WRONG").Publish(ctx, f.request(t, ""))
		if err == nil || !strings.Contains(err.Error(), "Bad credentials") || strings.Contains(err.Error(), "WRONG") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("unsupported host", func(t *testing.T) {
		f := newFixture(t)
		req := f.request(t, "")
		req.Repo = "https://gitlab.com/o/r"
		if _, err := f.publisher(secret).Publish(ctx, req); err == nil || !strings.Contains(err.Error(), "aren't supported") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("token never appears in git errors", func(t *testing.T) {
		f := newFixture(t)
		p := f.publisher(secret)
		p.RemoteURL = func(forge.Repo) string { return "file:///nonexistent/" + secret }
		_, err := p.Publish(ctx, f.request(t, ""))
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

// A follow-up adds a commit to the thread's branch and refreshes the
// description; the pull request is not opened again.
// A later push builds on the branch's head, adds one commit, and refreshes
// the pull request's description; commits someone else pushed are kept.
func TestPublishOnExistingBranch(t *testing.T) {
	f := newFixture(t)
	p := f.publisher(secret)
	ctx := context.Background()
	first, err := p.Publish(ctx, f.request(t, "## Done\nfirst"))
	if err != nil {
		t.Fatal(err)
	}

	// Someone pushes to the branch; then the next turn's changes, made
	// from the thread's own state (without that commit).
	root := filepath.Dir(f.remote)
	work := filepath.Join(root, "fu")
	sh(t, root, "git clone -q remote.git fu && git -C fu checkout -q "+first.Branch)
	patch := filepath.Join(root, "followup.patch")
	sh(t, work, `printf 'second round\n' >> new.txt && git add -A && git diff --cached --binary > `+patch+` && git reset -q --hard`)
	sh(t, work, "printf 'human\n' > human.txt && git add -A && git commit -qm human && git push -q origin HEAD:"+first.Branch)
	human := sh(t, work, "git rev-parse HEAD")

	req := task.PublishRequest{ThreadID: "t-20261015-1", Repo: "https://github.com/o/r", BaseCommit: f.base, Patches: []string{read(t, patch)},
		Summary: "## Done\nfirst and second", Requests: []string{"Tidy up the files", "also do more"}, Branch: first.Branch, PRNumber: 7}
	second, err := p.Publish(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if second != (task.Published{PRURL: "https://github.com/o/r/pull/7", PRNumber: 7, Branch: first.Branch}) {
		t.Errorf("second push %+v", second)
	}
	if len(f.pulls) != 1 || len(f.updates) != 1 {
		t.Fatalf("opened %d, updated %d; want 1 and 1", len(f.pulls), len(f.updates))
	}
	for _, s := range []string{"first and second", "Requests (2)", "1.\n> Tidy up the files", "2.\n> also do more"} {
		if !strings.Contains(f.updates[0]["body"], s) {
			t.Errorf("updated body lacks %q:\n%s", s, f.updates[0]["body"])
		}
	}
	check := t.TempDir()
	sh(t, check, "git clone -q "+f.remote+" . && git checkout -q "+first.Branch)
	if parent := sh(t, check, "git rev-parse HEAD~1"); parent != human {
		t.Errorf("new commit's parent = %s, want the branch head %s", parent, human)
	}
	if got := sh(t, check, "cat new.txt human.txt"); got != "new\nsecond round\nhuman" {
		t.Errorf("files = %q", got)
	}
	if msg := sh(t, check, "git log -1 --format=%B"); !strings.Contains(msg, "- also do more") {
		t.Errorf("commit message lacks the requests:\n%s", msg)
	}
}

// Several turns' changes are pushed as one commit.
func TestPublishSquashesTurns(t *testing.T) {
	f := newFixture(t)
	root := filepath.Dir(f.remote)
	sh(t, root, "git clone -q remote.git turns && git -C turns checkout -q "+f.base)
	work := filepath.Join(root, "turns")
	p1, p2 := filepath.Join(root, "1.patch"), filepath.Join(root, "2.patch")
	sh(t, work, `printf 'one\n' > one.txt && git add -A && git diff --cached --binary > `+p1+` && git -c user.name=t -c user.email=t@t commit -qm 1`)
	sh(t, work, `printf 'two\n' >> one.txt && printf 'x\n' > two.txt && git add -A && git diff --cached --binary > `+p2)

	req := f.request(t, "")
	req.Patches, req.OpenPR = []string{read(t, p1), read(t, p2)}, false
	pub, err := f.publisher(secret).Publish(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	check := t.TempDir()
	sh(t, check, "git clone -q "+f.remote+" . && git checkout -q "+pub.Branch)
	if n := sh(t, check, "git rev-list --count "+f.base+"..HEAD"); n != "1" {
		t.Errorf("%s commits on the branch, want 1", n)
	}
	if got := sh(t, check, "cat one.txt two.txt"); got != "one\ntwo\nx" {
		t.Errorf("files = %q", got)
	}
}

func TestIsOpen(t *testing.T) {
	f := newFixture(t)
	p := f.publisher(secret)
	if open, err := p.IsOpen(context.Background(), "https://github.com/o/r", 7, forge.Token{}); err != nil || !open {
		t.Errorf("open PR: %v, %v", open, err)
	}
	if open, err := p.IsOpen(context.Background(), "https://github.com/o/r", 8, forge.Token{}); err != nil || open {
		t.Errorf("merged PR: %v, %v", open, err)
	}
}

// Without a pull request only the branch is pushed; later, the pull
// request can be opened for it with nothing new to push.
func TestPublishBranchThenOpenPR(t *testing.T) {
	f := newFixture(t)
	p := f.publisher(secret)
	ctx := context.Background()
	req := f.request(t, "")
	req.OpenPR = false
	first, err := p.Publish(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first != (task.Published{Branch: Branch("t-20261015-1", prompt)}) || len(f.pulls) != 0 {
		t.Fatalf("branch only: published %+v, opened %d pull requests", first, len(f.pulls))
	}
	head := sh(t, t.TempDir(), "git ls-remote "+f.remote+" refs/heads/"+first.Branch+" | cut -f1")

	req.Patches, req.OpenPR, req.Branch = nil, true, first.Branch
	second, err := p.Publish(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if second != (task.Published{Branch: first.Branch, PRURL: "https://github.com/o/r/pull/7", PRNumber: 7}) {
		t.Errorf("opening the PR: %+v", second)
	}
	if len(f.pulls) != 1 || f.pulls[0]["head"] != first.Branch || f.pulls[0]["title"] != "Tidy up the files" {
		t.Errorf("pull requests opened: %v", f.pulls)
	}
	if now := sh(t, t.TempDir(), "git ls-remote "+f.remote+" refs/heads/"+first.Branch+" | cut -f1"); now != head {
		t.Errorf("the branch moved (%s → %s) with nothing to push", head, now)
	}
	// Nothing to push and no branch yet: an error, not an empty branch.
	req.Branch = ""
	if _, err := p.Publish(ctx, req); err == nil || !strings.Contains(err.Error(), "nothing to push") {
		t.Errorf("empty push: %v", err)
	}
}

// A signed-in user's token is used instead of the server's.
func TestPublishWithUserToken(t *testing.T) {
	f := newFixture(t)
	p := f.publisher("github_pat_WRONG") // the server's token would be refused
	req := f.request(t, "")
	req.Token = forge.NewToken(secret)
	if _, err := p.Publish(context.Background(), req); err != nil {
		t.Fatalf("user token not used: %v", err)
	}
	p.Tokens = nil // no server token at all
	if open, err := p.IsOpen(context.Background(), "https://github.com/o/r", 7, forge.NewToken(secret)); err != nil || !open {
		t.Errorf("IsOpen with the user's token: %v, %v", open, err)
	}
	if _, err := p.IsOpen(context.Background(), "https://github.com/o/r", 7, forge.Token{}); err == nil {
		t.Error("no token anywhere should fail")
	}
}
