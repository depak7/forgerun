package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/task"
)

// TestPublishToGitHub opens a real pull request. It needs a throwaway repo
// and a token for it:
//
//	FORGERUN_GITHUB_TEST_REPO=https://github.com/you/playground GITHUB_TOKEN=... go test ./internal/publish -run GitHub
func TestPublishToGitHub(t *testing.T) {
	repo, token := os.Getenv("FORGERUN_GITHUB_TEST_REPO"), os.Getenv("GITHUB_TOKEN")
	if repo == "" || token == "" {
		t.Skip("set FORGERUN_GITHUB_TEST_REPO and GITHUB_TOKEN to open a real pull request")
	}
	// A patch against the repo's current default branch, made like the runner makes one.
	dir := t.TempDir()
	base := sh(t, dir, "git clone -q --depth=1 "+repo+" work && git -C work rev-parse HEAD")
	patch := filepath.Join(dir, "changes.patch")
	sh(t, filepath.Join(dir, "work"), `printf 'Added by the forgerun publish test.\n' >> README.md &&
		printf 'new file\n' > forgerun-test.txt && git add -A && git diff --cached --binary > `+patch)

	p := &Publisher{Tokens: forge.NewStaticToken(forge.NewToken(token)), GitHub: &forge.GitHub{}}
	id := "test-" + time.Now().Format("20060102-150405") // a new branch every run
	pr, err := p.Publish(context.Background(), task.PublishRequest{
		TaskID: id, Repo: repo, BaseCommit: base, Patch: read(t, patch), OpenPR: true,
		Prompt:  "Publish test: append a line to the README",
		Summary: "## Requested\nAppend a line to the README.\n\n## Done\nAppended it and added forgerun-test.txt.\n\n## Open\nNothing; this is a test.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pr.PRURL, strings.TrimSuffix(repo, ".git")+"/pull/") || pr.Branch != Branch(id, "Publish test: append a line to the README") || pr.PRNumber == 0 {
		t.Errorf("published %+v", pr)
	}
	t.Logf("opened %s (branch %s)", pr.PRURL, pr.Branch)

	// A follow-up: a commit on top of the branch, and a refreshed description.
	work := filepath.Join(dir, "fu")
	head := sh(t, dir, "git clone -q --depth=1 --branch "+pr.Branch+" "+repo+" fu && git -C fu rev-parse HEAD")
	patch2 := filepath.Join(dir, "followup.patch")
	sh(t, work, `printf 'Second round.\n' >> forgerun-test.txt && git add -A && git diff --cached --binary > `+patch2)
	again, err := p.Publish(context.Background(), task.PublishRequest{
		TaskID: id + "-2", Repo: repo, BaseCommit: head, Patch: read(t, patch2),
		Prompt:   "Publish test: a second round",
		Summary:  "## Requested\nBoth rounds.\n\n## Done\nAppended a line, then a second round.\n\n## Open\nNothing.",
		Requests: []string{"Publish test: append a line to the README", "Publish test: a second round"},
		Branch:   pr.Branch, PRNumber: pr.PRNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.PRURL != pr.PRURL || again.PRNumber != pr.PRNumber {
		t.Errorf("follow-up published %+v, want the same pull request %+v", again, pr)
	}
	t.Logf("updated %s", again.PRURL)
}
