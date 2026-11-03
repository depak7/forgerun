package handoff

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func must(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// extract unpacks a packed folder into a new directory.
func extract(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return dir
		}
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, filepath.FromSlash(h.Name))
		if h.Typeflag == tar.TypeDir {
			os.MkdirAll(p, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		b, _ := io.ReadAll(tr)
		os.WriteFile(p, b, os.FileMode(h.Mode))
	}
}

func commit(t *testing.T, dir, msg string) {
	t.Helper()
	must(t, dir, "add", "-A")
	must(t, dir, "-c", "user.name=u", "-c", "user.email=u@x", "commit", "-q", "-m", msg)
}

// A repo is sent as its current commit (alone) plus its files as they are
// now: what git ignores and untracked secrets stay behind, and so do its
// other history, remotes and hooks.
func TestPackRepo(t *testing.T) {
	src := t.TempDir()
	must(t, src, "init", "-q", "-b", "main")
	write(t, src, "app.go", "package app\n")
	write(t, src, "gone.go", "package app\n")
	write(t, src, ".gitignore", "build/\n")
	write(t, src, "certs/test.pem", "tracked on purpose\n")
	commit(t, src, "one")
	write(t, src, "app.go", "package app\n// v2\n")
	commit(t, src, "two")
	base := must(t, src, "rev-parse", "HEAD")
	must(t, src, "remote", "add", "origin", "https://user:ghp_secret@github.com/o/r.git")

	write(t, src, "app.go", "package app\n// work in progress\n") // uncommitted
	write(t, src, "new.go", "package app\n")                      // untracked
	write(t, src, ".env", "KEY=real\n")                           // untracked secret
	write(t, src, "build/out", "x")                               // ignored
	os.Remove(filepath.Join(src, "gone.go"))                      // deleted, not committed

	pk, err := Pack(context.Background(), filepath.Join(src, "certs")) // from inside the repo
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(src)
	if pk.Root != real || pk.Base != base || pk.Branch != "main" || pk.Repo != "https://github.com/o/r" {
		t.Errorf("packed %+v", pk)
	}
	if !slices.Equal(pk.Skipped, []string{".env"}) {
		t.Errorf("skipped %v, want the untracked .env only", pk.Skipped)
	}

	dir := extract(t, pk.Data)
	if head := must(t, dir, "rev-parse", "HEAD"); head != base {
		t.Errorf("HEAD %s, want %s", head, base)
	}
	if n := must(t, dir, "rev-list", "--count", "HEAD"); n != "1" {
		t.Errorf("%s commits sent, want only the current one", n)
	}
	if remotes := must(t, dir, "remote"); remotes != "" {
		t.Errorf("remotes sent: %q", remotes)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".git", "config")); strings.Contains(string(b), "ghp_secret") {
		t.Error("the remote's credentials were sent")
	}
	for _, f := range []string{".env", "build/out"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s was sent", f)
		}
	}
	// What git sees is the work since the commit, as on this machine.
	must(t, dir, "add", "-A")
	if got := must(t, dir, "diff", "--cached", "--no-renames", "--name-status"); got != "M\tapp.go\nD\tgone.go\nA\tnew.go" {
		t.Errorf("changes since the commit:\n%s", got)
	}
}

func TestPackPlainFolderAndFile(t *testing.T) {
	src := t.TempDir()
	write(t, src, "notes.md", "# notes\n")
	write(t, src, "sub/run.sh", "echo hi\n")
	write(t, src, ".env.local", "KEY=x\n")
	write(t, src, ".env.example", "KEY=\n")
	pk, err := Pack(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if pk.Base != "" || pk.Repo != "" || pk.Files != 3 || !slices.Equal(pk.Skipped, []string{".env.local"}) {
		t.Errorf("packed %+v", pk)
	}
	dir := extract(t, pk.Data)
	if _, err := os.Stat(filepath.Join(dir, "sub", "run.sh")); err != nil {
		t.Error(err)
	}

	one, err := Pack(context.Background(), filepath.Join(src, "notes.md"))
	if err != nil || one.Name != "notes.md" || one.Files != 1 {
		t.Fatalf("one file: %+v, %v", one, err)
	}
	if b, _ := os.ReadFile(filepath.Join(extract(t, one.Data), "notes.md")); string(b) != "# notes\n" {
		t.Errorf("file = %q", b)
	}
}

func TestSecret(t *testing.T) {
	for name, want := range map[string]bool{
		".env": true, ".env.production": true, ".env.example": false, ".ENV.Sample": false,
		"id_rsa": true, "id_ed25519.pub": false, "server.key": true, "cert.pem": true,
		".npmrc": true, "main.go": false, "environment.ts": false, "keys.go": false,
	} {
		if got := secret(name); got != want {
			t.Errorf("secret(%q) = %v", name, got)
		}
	}
}

func TestSplitRemote(t *testing.T) {
	for u, want := range map[string][2]string{
		"https://github.com/o/r.git":           {"github.com", "o/r.git"},
		"https://x:tok@GitHub.com/o/r":         {"github.com", "o/r"},
		"git@github.com:o/r.git":               {"github.com", "o/r.git"},
		"ssh://git@github.com:22/o/r.git":      {"github.com", "o/r.git"},
		"git@github-personal:depak7/amika.git": {"github-personal", "depak7/amika.git"},
	} {
		host, p, ok := splitRemote(u)
		if !ok || host != want[0] || p != want[1] {
			t.Errorf("splitRemote(%q) = %q, %q, %v", u, host, p, ok)
		}
	}
	for _, u := range []string{"/srv/repo.git", "../other", "file:///srv/r"} {
		if _, _, ok := splitRemote(u); ok {
			t.Errorf("splitRemote(%q) took a local path", u)
		}
	}
}
