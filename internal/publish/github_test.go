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
	url, err := p.Publish(context.Background(), task.PublishRequest{
		TaskID: id, Repo: repo, BaseCommit: base, PatchPath: patch,
		Prompt:  "Publish test: append a line to the README",
		Summary: "## Requested\nAppend a line to the README.\n\n## Done\nAppended it and added forgerun-test.txt.\n\n## Open\nNothing; this is a test.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, strings.TrimSuffix(repo, ".git")+"/pull/") {
		t.Errorf("url = %q", url)
	}
	t.Logf("opened %s (branch %s)", url, Branch(id))
}
